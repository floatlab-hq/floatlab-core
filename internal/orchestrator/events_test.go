package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestNodeEventsSurviveReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(db)
	if err := store.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(ctx, db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	startHost := func(socket string) (*ipc.Server, func()) {
		t.Helper()
		hostCtx, stopHost := context.WithCancel(ctx)
		t.Cleanup(stopHost)
		server := ipc.NewServer(socket, zap.NewNop())
		server.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) {
			return ipc.DockerListResult{Containers: []ipc.ContainerInfo{{ID: "dead", State: "exited"}}}, nil
		})
		hostDone := make(chan error, 1)
		go func() { hostDone <- server.Run(hostCtx) }()
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, err := os.Stat(socket); err == nil {
				return server, func() {
					stopHost()
					select {
					case err := <-hostDone:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(3 * time.Second):
						t.Fatal("host did not stop")
					}
				}
			}
			if time.Now().After(deadline) {
				t.Fatal("host did not start")
			}
			time.Sleep(time.Millisecond)
		}
	}
	firstSocket := filepath.Join(t.TempDir(), "first.sock")
	first, stopFirst := startHost(firstSocket)
	// Register before Run, without opening a connection or emitting a config event.
	pool.Register("node", firstSocket)
	orch := New(store, node, pool, db, zap.NewNop())
	done := make(chan error, 1)
	runCtx, stopRun := context.WithCancel(ctx)
	go func() { done <- orch.Run(runCtx) }()
	defer func() {
		stopRun()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("orchestrator did not release node followers on cancellation")
		}
	}()
	checkDelivery := func(server *ipc.Server) {
		t.Helper()
		if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
			t.Fatal(err)
		}
		deadline := time.NewTimer(4 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			server.EmitContext(ctx, "container.state", ipc.ContainerStateEvent{StackID: "web", ContainerID: "dead", Status: "die"})
			if state, _ := node.FSM().State("web"); state.State == run.StateFailed {
				return
			}
			select {
			case <-deadline.C:
				t.Fatal("registered host's container event never reached the lifecycle machine")
			case <-ticker.C:
			}
		}
	}
	checkDelivery(first)
	stopRun()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("orchestrator did not stop")
	}
	orch.mu.Lock()
	remaining := len(orch.nodeEvents)
	orch.mu.Unlock()
	if remaining != 0 {
		t.Fatal("stopped orchestrator retained event followers")
	}
	runCtx, stopRun = context.WithCancel(ctx)
	defer stopRun()
	go func() { done <- orch.Run(runCtx) }()
	checkDelivery(first)
	// Restart the host at the same registered address, without an RPC or config
	// event to evict its old client. Its old Events channel stays open.
	stopFirst()
	second, _ := startHost(firstSocket)
	checkDelivery(second)
}
