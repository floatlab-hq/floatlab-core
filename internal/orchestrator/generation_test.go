package orchestrator

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/operation"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestStaleStartFailureDoesNotFinishLaterOperation(t *testing.T) {
	ctx := t.Context()
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
	cfg := config.NewStore(db)
	if err := cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "node", ComposeYAML: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(ctx, db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Unix(100, 0).UTC()
	current := observed.Add(time.Second)
	if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateStarting, Timestamp: current}, time.Second); err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	orch := New(cfg, node, pool, db, zap.NewNop())
	if err := orch.ops.Create(ctx, operation.Operation{ID: "new-start", StackID: "web", Action: "start", Actor: "test", State: "running", Checkpoint: "running", CreatedAt: current, UpdatedAt: current}); err != nil {
		t.Fatal(err)
	}
	// The old goroutine's host command fails after a new start owns the stack.
	// An unregistered host makes that failure deterministic without a daemon.
	orch.doStart(ctx, "web", observed)
	op, err := orch.ops.Get(ctx, "new-start")
	if err != nil {
		t.Fatal(err)
	}
	state, _ := node.FSM().State("web")
	if op.State != "running" || state.State != run.StateStarting || !state.UpdatedAt.Equal(current) {
		t.Fatalf("old action completed new operation: op=%+v state=%+v", op, state)
	}
}
