package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func reconciliationOrchestrator(t *testing.T) *Orchestrator {
	t.Helper()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := rqlite.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(t.Context(), db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	t.Cleanup(pool.Close)
	return New(config.NewStore(db), node, pool, db, zap.NewNop())
}

func TestReconciliationRecoversDroppedTransition(t *testing.T) {
	orch := reconciliationOrchestrator(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for _, id := range []string{"blocker", "target"} {
		if err := orch.store.CreateStack(ctx, &config.Stack{ID: id, Name: id, PrimaryNodeID: "node", ComposeYAML: "services:\n  app: {image: alpine}\n"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := orch.raft.Apply(run.StackStateChanged{StackID: "blocker", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	blocked, release, started := make(chan struct{}), make(chan struct{}), make(chan struct{}, 4)
	defer close(release)
	daemon.Handle("docker.list", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(blocked)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return ipc.DockerListResult{Containers: []ipc.ContainerInfo{{ID: "blocker-container", State: "running"}}}, nil
	})
	var calls atomic.Int32
	daemon.Handle("compose.up", func(context.Context, json.RawMessage) (any, error) {
		calls.Add(1)
		started <- struct{}{}
		return nil, nil
	})
	go func() { _ = daemon.Run(ctx) }()
	orch.pool.Register("node", socket)
	ready := time.Now().Add(time.Second)
	for {
		if _, unsubscribe, err := orch.pool.SubscribeEvents(ctx, "node"); err == nil {
			unsubscribe()
			break
		}
		if time.Now().After(ready) {
			t.Fatal("IPC server did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("orchestrator did not stop")
		}
	})
	payload, _ := json.Marshal(ipc.ContainerStateEvent{StackID: "blocker", ContainerID: "blocker-container", Status: "die"})
	orch.merged <- ipc.Event{Name: "container.state", Payload: payload}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("event loop did not enter blocking health query")
	}
	now := time.Now().UTC()
	if err := orch.ops.Create(ctx, operation.Operation{ID: "target-start", StackID: "target", Action: "start", Actor: "test", State: "running", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Every subscriber has the same 64-entry buffer. The event loop is blocked,
	// so the final target transition is dropped rather than queued for dispatch.
	observed, unsubscribe := orch.raft.FSM().Subscribe()
	defer unsubscribe()
	for i := range 64 {
		if err := orch.raft.Apply(run.StackStateChanged{StackID: fmt.Sprintf("filler-%d", i), To: run.StateIdle, Timestamp: time.Now().UTC()}, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if err := orch.raft.Apply(run.StackStateChanged{StackID: "target", From: run.StateIdle, To: run.StateStarting, Event: run.EventStartStack, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	for len(observed) > 0 {
		if event := <-observed; event.StackID == "target" {
			t.Fatal("test did not overflow the subscription")
		}
	}
	release <- struct{}{}
	select {
	case <-started:
	case <-time.After(8 * time.Second):
		t.Fatal("dropped Starting transition was never reconciled")
	}
	deadline := time.Now().Add(time.Second)
	for {
		op, err := orch.ops.Get(ctx, "target-start")
		if err != nil {
			t.Fatal(err)
		}
		if op.State == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reconciled start did not finish its operation")
		}
		time.Sleep(time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("start dispatched %d times", calls.Load())
	}
}
