package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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

func TestStartUsesProjectComposeWithRemovedBackupTarget(t *testing.T) {
	ctx := t.Context()
	orch, generation := stopFixture(t)
	const draft = `name: web
services:
  web:
    image: alpine
    volumes:
      - type: bind
        source: ./data
        target: /data
        x-fl-backups:
          - node_id: backup
            schedule: 1h/2
`
	var source string
	orch.store = config.NewProjectStore(orch.db, nil, func(context.Context, *config.Stack) (string, error) {
		return source, nil
	}, func(_ context.Context, stack *config.Stack) error {
		source = stack.ComposeYAML
		return nil
	})
	if err := orch.store.UpdateStackCompose(ctx, "web", draft); err != nil {
		t.Fatal(err)
	}
	stack, err := orch.store.CatalogStack(ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if err := orch.store.PublishStackCompose(ctx, stack); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.DisableBackupNode(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	// A user edits the on-disk project while the stack is stopped.
	source = strings.Replace(source, "image: alpine", "image: busybox", 1)
	generation, applied := orch.applyEvent("node", "web", run.EventStopDone, generation)
	if !applied {
		t.Fatal("transition to idle rejected")
	}
	generation, applied = orch.applyEvent("node", "web", run.EventStartStack, generation)
	if !applied {
		t.Fatal("transition to starting rejected")
	}
	started := make(chan ipc.ComposeUpPayload, 1)
	stackHost(t, orch, "compose.up", func(_ context.Context, raw json.RawMessage) (any, error) {
		var payload ipc.ComposeUpPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		started <- payload
		return nil, nil
	})
	orch.doStart(ctx, "web", generation)
	if state, _ := orch.raft.FSM().State("web"); state.State != run.StateRunningPrimary {
		t.Fatalf("start failed with removed backup target: %+v", state)
	}
	select {
	case payload := <-started:
		if !strings.Contains(payload.ComposeFile, "image: busybox") {
			t.Fatalf("started with stale source: %s", payload.ComposeFile)
		}
	default:
		t.Fatal("host did not start the stack")
	}
}
