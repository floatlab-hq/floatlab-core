package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
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

func stopFixture(t *testing.T) (*Orchestrator, time.Time) {
	t.Helper()
	ctx := t.Context()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewStore(db)
	if err = cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "node", ZFSDataset: "floatlab/web", ComposeYAML: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(ctx, db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	generation := time.Now().UTC()
	if err = node.Apply(run.StackStateChanged{StackID: "web", To: run.StateStopping, Timestamp: generation}, time.Second); err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	t.Cleanup(pool.Close)
	orch := New(cfg, node, pool, db, zap.NewNop())
	if err = orch.ops.Create(ctx, operation.Operation{ID: "stop", StackID: "web", Action: "stop", Actor: "test", State: "running", Checkpoint: "running", CreatedAt: generation, UpdatedAt: generation}); err != nil {
		t.Fatal(err)
	}
	return orch, generation
}

func stopHost(t *testing.T, orch *Orchestrator, handler ipc.Handler) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	socket := filepath.Join(t.TempDir(), "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	server.Handle("compose.down", handler)
	server.Handle("probe", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	go func() { _ = server.Run(ctx) }()
	orch.pool.Register("node", socket)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := orch.pool.Execute(ctx, "node", "probe", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not start")
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("host did not start")
		}
	}
	return cancel
}

func TestStopFailureCanBeRetriedAfterRestart(t *testing.T) {
	orch, generation := stopFixture(t)
	stopHost(t, orch, func(context.Context, json.RawMessage) (any, error) { return nil, errors.New("compose down failed") })
	orch.doStop(t.Context(), "web", generation)
	state, _ := orch.raft.FSM().State("web")
	op, err := orch.ops.Get(t.Context(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if state.State != run.StateFailed || op.State != "failed" {
		t.Fatalf("stop stranded: state=%+v operation=%+v", state, op)
	}

	restarted, err := floatraft.NewLocalNode(t.Context(), orch.db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	orch = New(orch.store, restarted, orch.pool, orch.db, zap.NewNop())
	state, _ = restarted.FSM().State("web")
	next, err := run.New().Apply(t.Context(), state, run.EventStopStack)
	if err != nil {
		t.Fatalf("failed stop cannot retry after restart: %v", err)
	}
	if err = orch.ops.Create(t.Context(), operation.Operation{ID: "retry", StackID: "web", Action: "stop", Actor: "test", State: "running", CreatedAt: next.UpdatedAt, UpdatedAt: next.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if err = restarted.Apply(run.StackStateChanged{StackID: "web", From: state.State, To: next.State, Event: run.EventStopStack, ExpectedUpdatedAt: state.UpdatedAt, Timestamp: next.UpdatedAt}, time.Second); err != nil {
		t.Fatal(err)
	}
	stopHost(t, orch, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	orch.doStop(t.Context(), "web", next.UpdatedAt)
	state, _ = restarted.FSM().State("web")
	op, _ = orch.ops.Get(t.Context(), "retry")
	if state.State != run.StateIdle || op.State != "succeeded" {
		t.Fatalf("retry did not finish: state=%+v operation=%+v", state, op)
	}
}

func TestStaleStopFailureDoesNotFinishLaterOperation(t *testing.T) {
	orch, generation := stopFixture(t)
	orch.doStop(t.Context(), "web", generation.Add(-time.Second))
	state, _ := orch.raft.FSM().State("web")
	op, err := orch.ops.Get(t.Context(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if state.State != run.StateStopping || op.State != "running" || op.Checkpoint != "running" {
		t.Fatalf("old stop failed a later operation: state=%+v operation=%+v", state, op)
	}
}

func TestCancelledStopRemainsRecoverableAfterRestart(t *testing.T) {
	orch, generation := stopFixture(t)
	entered := make(chan struct{})
	var attempts atomic.Int32
	stopHost(t, orch, func(ctx context.Context, _ json.RawMessage) (any, error) {
		if attempts.Add(1) == 1 {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return nil, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); orch.doStop(ctx, "web", generation) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stop did not reach host")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled stop did not return")
	}
	op, err := orch.ops.Get(t.Context(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "running" {
		t.Fatalf("cancelled controller abandoned durable stop: %+v", op)
	}
	restarted, err := floatraft.NewLocalNode(t.Context(), orch.db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	orch = New(orch.store, restarted, orch.pool, orch.db, zap.NewNop())
	orch.doStop(t.Context(), "web", generation)
	state, _ := restarted.FSM().State("web")
	op, _ = orch.ops.Get(t.Context(), "stop")
	if state.State != run.StateIdle || op.State != "succeeded" {
		t.Fatalf("restart did not resume stop: state=%+v operation=%+v", state, op)
	}
}

func TestUnknownStopKeepsReservationAcrossRestart(t *testing.T) {
	orch, generation := stopFixture(t)
	entered := make(chan struct{})
	stopDaemon := stopHost(t, orch, func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); orch.doStop(ctx, "web", generation) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("stop did not reach host")
	}
	stopDaemon()
	select {
	case <-done:
		t.Fatal("transport loss released the stop reservation")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("controller cancellation did not stop retry")
	}

	restarted, err := floatraft.NewLocalNode(t.Context(), orch.db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	t.Cleanup(pool.Close)
	orch = New(orch.store, restarted, pool, orch.db, zap.NewNop())
	done = make(chan struct{})
	go func() { defer close(done); orch.doStop(t.Context(), "web", generation) }()
	select {
	case <-done:
		t.Fatal("restarted stop forgot its uncertain prior dispatch")
	case <-time.After(100 * time.Millisecond):
	}
	op, err := orch.ops.Get(t.Context(), "stop")
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "running" {
		t.Fatalf("uncertain stop released reservation: %+v", op)
	}
	if err := orch.ops.Create(t.Context(), operation.Operation{ID: "unsafe-start", StackID: "web", Action: "start", Actor: "test", State: "pending", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err == nil {
		t.Fatal("uncertain stop admitted another mutation")
	}
	stopHost(t, orch, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("stop did not retry after host recovery")
	}
	state, _ := restarted.FSM().State("web")
	op, _ = orch.ops.Get(t.Context(), "stop")
	if state.State != run.StateIdle || op.State != "succeeded" {
		t.Fatalf("confirmed retry did not finish: state=%+v operation=%+v", state, op)
	}
}
