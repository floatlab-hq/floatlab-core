package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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

func upgradeFixture(t *testing.T, pullError ...error) (*Worker, StackUpgradePayload, *atomic.Int32) {
	t.Helper()
	ctx := t.Context()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "upgrade.sqlite"), "node")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewStore(db)
	const old = "services:\n  app: {image: 'alpine:3.20'}\n"
	const updated = "services:\n  app: {image: 'alpine:3.21'}\n"
	if err := cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "node", ZFSDataset: "floatlab/web", ComposeYAML: old}); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(ctx, db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC()
	if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningPrimary, Timestamp: observed, NodeID: "node"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := operation.NewStore(db).Create(ctx, operation.Operation{ID: "op", StackID: "web", Action: "upgrade", Actor: "test", State: "running", Checkpoint: "running", CreatedAt: observed, UpdatedAt: observed}); err != nil {
		t.Fatal(err)
	}
	p := StackUpgradePayload{OperationID: "op", StackID: "web", NodeID: "node", DatasetPath: "floatlab/web", OldCompose: old, NewCompose: updated, HealthTimeout: "1s", Lifecycle: run.StackStateChanged{StackID: "web", From: run.StateRunningPrimary, ExpectedUpdatedAt: observed, To: run.StateUpdating, Event: run.EventUpdateStack, Timestamp: observed.Add(time.Second), NodeID: "node"}}
	if err := EnqueueTask(ctx, db, "upgrade-op", TaskStackUpgrade, "web", p); err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp("/tmp", "upgrade-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(directory) })
	socket := filepath.Join(directory, "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	calls := &atomic.Int32{}
	for _, command := range []string{"compose.pull", "fs.snapshot.create", "compose.source.write", "compose.up"} {
		server.Handle(command, func(context.Context, json.RawMessage) (any, error) {
			calls.Add(1)
			if command == "compose.pull" && len(pullError) != 0 {
				return nil, pullError[0]
			}
			return nil, nil
		})
	}
	server.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) {
		return ipc.DockerListResult{Containers: []ipc.ContainerInfo{{State: "running", Health: "healthy"}}}, nil
	})
	daemonCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Run(daemonCtx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host socket did not start")
		}
		time.Sleep(time.Millisecond)
	}
	pool := hostclient.NewPool(zap.NewNop())
	t.Cleanup(pool.Close)
	pool.Register("node", socket)
	w := New(db, cfg, pool, node, zap.NewNop())
	return w, p, calls
}

func TestFailedAutomaticUpgradeAlertsAndPersistsCandidate(t *testing.T) {
	w, p, calls := upgradeFixture(t, errors.New("registry refused candidate"))
	p.Candidate = "candidate-version"
	if err := w.saveUpgrade(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	if err := w.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("failed image pull changed the live stack: %d host calls", calls.Load())
	}
	op, err := w.ops.Get(t.Context(), p.OperationID)
	if err != nil || op.State != "failed" || op.Checkpoint != "rolled-back" {
		t.Fatalf("operation=%+v err=%v", op, err)
	}
	rows, err := w.db.Query(t.Context(), rqlite.Statement{SQL: `SELECT a.message,f.candidate FROM alerts a JOIN failed_workload_upgrades f ON f.stack_id=a.stack_id WHERE a.stack_id=? AND a.kind='failure'`, Params: []any{p.StackID}})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][1] != p.Candidate {
		t.Fatalf("failure not recorded: rows=%+v err=%v", rows, err)
	}
}

func TestUpgradeDurableIntentSurvivesRestartAndCompletionRetry(t *testing.T) {
	w, p, calls := upgradeFixture(t)
	before, _ := w.raft.FSM().State(p.StackID)
	if before.State != run.StateRunningPrimary {
		t.Fatal("admission changed lifecycle before worker")
	}
	// Reconstruct the lifecycle authority after the request has durably enqueued its intent.
	restarted, err := floatraft.NewLocalNode(t.Context(), w.db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	w.raft = restarted
	if err := w.db.Execute(t.Context(), []rqlite.Statement{{SQL: `CREATE TRIGGER reject_upgrade_finish BEFORE UPDATE OF state ON operations WHEN NEW.state='succeeded' BEGIN SELECT RAISE(ABORT,'finish unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	if err := w.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, _ := w.raft.FSM().State(p.StackID)
	if state.State != run.StateRunningPrimary || state.PreviousState != run.StateUpdating {
		t.Fatalf("worker did not complete upgrade: %+v", state)
	}
	op, err := w.ops.Get(t.Context(), p.OperationID)
	if err != nil || op.State != "running" {
		t.Fatalf("lost pending finalization: %+v %v", op, err)
	}
	originalCalls := calls.Load()
	if originalCalls != 4 {
		t.Fatalf("unexpected upgrade commands %d", originalCalls)
	}
	if err := w.db.Execute(t.Context(), []rqlite.Statement{{SQL: `DROP TRIGGER reject_upgrade_finish`}, {SQL: `UPDATE tasks SET updated_at=datetime('now','-120 seconds') WHERE id='upgrade-op'`}}); err != nil {
		t.Fatal(err)
	}
	restarted, err = floatraft.NewLocalNode(t.Context(), w.db, "node", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	w.raft = restarted
	if err := w.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	op, err = w.ops.Get(t.Context(), p.OperationID)
	if err != nil || op.State != "succeeded" {
		t.Fatalf("completion intent did not resume: %+v %v", op, err)
	}
	if calls.Load() != originalCalls {
		t.Fatal("completion retry repeated host mutations")
	}
}

func TestUpgradeIntentCannotAdoptLaterUpdatingGeneration(t *testing.T) {
	w, p, calls := upgradeFixture(t)
	newer := p.Lifecycle.Timestamp.Add(time.Second)
	if err := w.raft.Apply(run.StackStateChanged{StackID: p.StackID, To: run.StateUpdating, Timestamp: newer, NodeID: "node"}, time.Second); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	if err := w.handleStackUpgrade(t.Context(), raw); !errors.Is(err, run.ErrInvalidTransition) {
		t.Fatalf("stale task accepted: %v", err)
	}
	state, _ := w.raft.FSM().State(p.StackID)
	if !state.UpdatedAt.Equal(newer) || state.State != run.StateUpdating || calls.Load() != 0 {
		t.Fatalf("old intent changed later operation: %+v calls=%d", state, calls.Load())
	}
}

func TestExhaustedBackupRestoreCannotFailLaterGeneration(t *testing.T) {
	w, p, _ := upgradeFixture(t)
	if err := w.db.Execute(t.Context(), []rqlite.Statement{{SQL: `DELETE FROM tasks`}}); err != nil {
		t.Fatal(err)
	}
	old := p.Lifecycle.Timestamp
	newer := old.Add(time.Second)
	if err := w.raft.Apply(run.StackStateChanged{StackID: p.StackID, To: run.StateRestoring, Timestamp: newer, NodeID: "node"}, time.Second); err != nil {
		t.Fatal(err)
	}
	restore := BackupRestorePayload{OperationID: p.OperationID, StackID: p.StackID, DestinationNodeID: "node", Lifecycle: run.StackStateChanged{StackID: p.StackID, From: run.StateIdle, To: run.StateRestoring, Timestamp: old, NodeID: "node", Event: run.EventBackupRestoreStart}}
	if err := EnqueueTask(t.Context(), w.db, "backup-restore-op", TaskBackupRestore, p.StackID, restore); err != nil {
		t.Fatal(err)
	}
	if err := w.db.Execute(t.Context(), []rqlite.Statement{{SQL: `UPDATE tasks SET attempts=4,updated_at=datetime('now','-600 seconds')`}}); err != nil {
		t.Fatal(err)
	}
	if err := w.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, _ := w.raft.FSM().State(p.StackID)
	if state.State != run.StateRestoring || !state.UpdatedAt.Equal(newer) {
		t.Fatalf("exhausted old restore failed later generation: %+v", state)
	}
}
