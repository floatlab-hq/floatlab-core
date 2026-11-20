package control

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/failover"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestStandaloneLegacyFailbackRequiresCoordinatedHandoff(t *testing.T) {
	ctx := context.Background()
	server, db := peerWorkloadServer(t, "backup")
	if err := server.store.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "primary", BackupNodeID: "backup"}); err != nil {
		t.Fatal(err)
	}
	if err := server.store.CreateNode(ctx, &config.Node{ID: "backup", Name: "backup"}); err != nil {
		t.Fatal(err)
	}
	if err := server.raft.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningBackup, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	server.seq = failover.NewSequence(db, server.store, server.raft, hosts, nil, zap.NewNop())
	before, _ := server.raft.FSM().State("web")
	for _, request := range []struct{ path, body string }{
		{"/api/v1/stacks/web/restore", ""},
		{"/api/v1/stacks/web/failover", `{"action":"restore"}`},
	} {
		response := peerRequest(t, server, http.MethodPost, request.path, request.body)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "coordinated_takeover_required") {
			t.Errorf("legacy failback accepted: %s %d %s", request.path, response.Code, response.Body.String())
		}
		after, _ := server.raft.FSM().State("web")
		if *after != *before {
			t.Errorf("rejected failback changed lifecycle: %+v", after)
		}
	}
}
