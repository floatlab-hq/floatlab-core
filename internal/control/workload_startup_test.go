package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func TestManagedStartupRestoresLocalWorkloadOrKeepsItFenced(t *testing.T) {
	for _, scenario := range []struct {
		name string
		peer *workloadStartupStatus
	}{
		{name: "unreachable peer"},
		{name: "peer has an active copy", peer: &workloadStartupStatus{Running: true}},
		{name: "peer owns the stack", peer: &workloadStartupStatus{Owner: "other", Generation: 1}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s, db := peerWorkloadServer(t, "local")
			stack := &config.Stack{
				ID: uuid.NewString(), Name: "test", PrimaryNodeID: "local", ZFSDataset: "floatlab/test",
				ComposeYAML: "name: test\nservices:\n  app:\n    image: alpine\n",
			}
			if err := s.store.CreateStack(ctx, stack); err != nil {
				t.Fatal(err)
			}
			if err := s.raft.Apply(run.StackStateChanged{StackID: stack.ID, To: run.StateRunningPrimary, Timestamp: time.Now().UTC(), NodeID: "local"}, time.Second); err != nil {
				t.Fatal(err)
			}
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != workloadStartupPath+stack.ID {
					http.NotFound(w, r)
					return
				}
				writeJSON(w, http.StatusOK, scenario.peer)
			}))
			defer peer.Close()
			if scenario.peer == nil {
				peer.Close()
			}
			if err := s.SetWorkloadPeers(t.TempDir(), peer.Client(), func(string) (string, bool) { return peer.URL, true }); err != nil {
				t.Fatal(err)
			}
			s.SetWorkloadStartupPeers(func() []string { return []string{"other"} })

			socket := filepath.Join(t.TempDir(), "host.sock")
			daemon := ipc.NewServer(socket, zap.NewNop())
			var starts atomic.Int32
			var stops atomic.Int32
			daemon.Handle("docker.startup.guard", func(context.Context, json.RawMessage) (any, error) { return true, nil })
			daemon.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) {
				result := ipc.DockerListResult{}
				if starts.Load() > stops.Load() {
					result.Containers = []ipc.ContainerInfo{{State: "running"}}
				}
				return result, nil
			})
			daemon.Handle("docker.startup.restore", func(context.Context, json.RawMessage) (any, error) { return false, nil })
			daemon.Handle("compose.up", func(context.Context, json.RawMessage) (any, error) { starts.Add(1); return nil, nil })
			daemon.Handle("compose.down", func(context.Context, json.RawMessage) (any, error) { stops.Add(1); return nil, nil })
			done := make(chan error, 1)
			go func() { done <- daemon.Run(ctx) }()
			waitForSocket(t, socket)
			s.hosts = hostclient.NewPool(zap.NewNop())
			defer func() { cancel(); <-done; s.hosts.Close() }()
			s.hosts.Register("local", socket)
			if err := s.EnableWorkloadStartup(ctx); err != nil {
				t.Fatal(err)
			}
			s.hosts.SetWorkloadStartCheck(s.CheckWorkloadStart)
			if err := s.CheckWorkloadStart(ctx, stack.ID); err == nil {
				t.Fatal("start allowed during startup delay")
			}
			s.startup.pending.Store(stack.ID, startupCandidate{})
			s.startup.ready.Store(true)
			if scenario.peer == nil {
				created := time.Now().UTC()
				op := operation.Operation{ID: "snapshot", StackID: stack.ID, Action: "snapshots", State: "running", CreatedAt: created, UpdatedAt: created}
				if err := s.ops.Create(ctx, op); err != nil {
					t.Fatal(err)
				}
				if err := s.RestoreWorkloadsOnStartup(ctx); err != nil || starts.Load() != 0 {
					t.Fatalf("startup must wait for the active operation: %v", err)
				}
				if err := s.CheckWorkloadStart(ctx, stack.ID); err != nil {
					t.Fatalf("the active operation must be able to finish: %v", err)
				}
				if err := s.ops.Update(ctx, op.ID, "succeeded", "succeeded", ""); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.RestoreWorkloadsOnStartup(ctx); err != nil {
				t.Fatal(err)
			}
			if got := starts.Load(); (scenario.peer == nil && got != 1) || (scenario.peer != nil && got != 0) {
				t.Fatalf("unexpected starts: %d", got)
			}
			if scenario.peer == nil {
				if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO workload_move_owners(stack_id,node_id,generation) VALUES(?,'other',1)`, Params: []any{stack.ID}}}); err != nil {
					t.Fatal(err)
				}
				if err := s.RestoreWorkloadsOnStartup(ctx); err != nil || stops.Load() != 1 {
					t.Fatalf("did not stop after observing the new owner: %v", err)
				}
			}
			if scenario.peer != nil && scenario.peer.Owner != "" {
				peer.Close()
				owner, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT node_id FROM workload_move_owners WHERE stack_id=?`, Params: []any{stack.ID}})
				if err != nil || len(owner.Values) != 1 || owner.Values[0][0] != "other" {
					t.Fatalf("ownership fence missing: %+v, %v", owner, err)
				}
				if err := s.CheckWorkloadStart(ctx, stack.ID); err == nil {
					t.Fatal("start allowed after the peer went offline")
				}
			}
		})
	}
}
