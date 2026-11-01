package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestStorageSnapshotMetadataAndPurposeSurviveIPC(t *testing.T) {
	database := testDatabase(t, stackRow("stack-1", "demo", "node-a", "node-b"), nil)
	defer database.Close()
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	host := ipc.NewServer(socket, zap.NewNop())
	host.Handle("fs.snapshot.list", func(context.Context, json.RawMessage) (any, error) {
		return ipc.SnapshotListResult{Snapshots: []ipc.SnapshotInfoResult{
			{Name: "fl-0J8F40", Dataset: "floatlab/demo/data", CreatedAt: "2026-10-28T00:00:00Z", Purpose: "replication", OperationID: "run-1", HoldTargets: []string{"node-b", "node-c"}, TransferTargets: []string{"node-d"}, Holds: []string{"floatlab:hold:node-b", "external-owner"}},
			{Name: "fl-0J8F41", Dataset: "floatlab/demo/data", CreatedAt: "2026-10-28T00:00:03Z", Purpose: "scheduled"},
		}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go host.Run(ctx)
	waitForSocket(t, socket)
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	hosts.Register("node-a", socket)
	s := &Server{store: config.NewStore(rqlite.NewClient(database.URL)), hosts: hosts}
	request := httptest.NewRequest("GET", "/storage/datasets/stack-1/snapshots?kind=replication", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("stack_id", "stack-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	s.handleListSnapshots(response, request)
	if response.Code != 200 {
		t.Fatalf("%d %s", response.Code, response.Body.String())
	}
	var snapshots []snapshotResponse
	if err := json.Unmarshal(response.Body.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("purpose filter lost: %+v", snapshots)
	}
	snap := snapshots[0]
	if snap.OperationID != "run-1" || snap.Purpose != "replication" || len(snap.HoldTargets) != 2 || len(snap.TransferTargets) != 1 || len(snap.Holds) != 2 {
		t.Fatalf("lost protection metadata: %+v", snap)
	}
}
