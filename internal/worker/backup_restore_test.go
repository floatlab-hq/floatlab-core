package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
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

func TestBackupRestoreResumesDurableActivatedMountsBeforeProvisioning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "restore.sqlite"), "destination")
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
	store := config.NewStore(db)
	if err := store.CreateNode(ctx, &config.Node{ID: "destination", Name: "destination", Addresses: []config.NodeAddress{{Type: "LAN-6", Address: "fd00::3"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateStack(ctx, &config.Stack{ID: "new-stack", Name: "restored", PrimaryNodeID: "destination", ZFSDataset: "floatlab/restored", ComposeYAML: "name: restored\nservices:\n  app:\n    image: alpine\n"}); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := floatraft.NewLocalNode(ctx, db, "destination", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Apply(run.StackStateChanged{StackID: "new-stack", NodeID: "destination", To: run.StateRestoring, Event: run.EventBackupRestoreStart, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	ops := operation.NewStore(db)
	if err := ops.Create(ctx, operation.Operation{ID: "restore-op", StackID: "new-stack", Action: "start", State: "pending", Checkpoint: "accepted", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	payload := BackupRestorePayload{OperationID: "restore-op", StackID: "new-stack", SourceNodeID: "offline-source", BackupNodeID: "backup", DestinationNodeID: "destination", DatasetPath: "floatlab/restored", Snapshots: []BackupRestoreSnapshot{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}}}
	if err := EnqueueTask(ctx, db, "backup-restore-restore-op", TaskBackupRestore, payload.StackID, payload); err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "restore-worker-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	activations := map[string]int{}
	daemon.Handle("fs.backup.restore.reserve", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p ipc.BackupRestoreReservePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.OperationID != payload.OperationID || p.TargetDataset != payload.DatasetPath {
			return nil, errors.New("wrong restore reservation")
		}
		return nil, nil
	})
	daemon.Handle("fs.backup.restore.activate", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p ipc.BackupRestoreActivatePayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if p.SourceNodeID != "backup" || p.OperationID != payload.OperationID || p.TargetDataset != "floatlab/restored/"+p.MountID || p.Mountpoint != "/"+p.TargetDataset || p.ExpectedGUID != "123" {
			return nil, errors.New("wrong activation identity or mountpoint")
		}
		activations[p.MountID]++
		return nil, nil
	})
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hostd test IPC did not start")
		}
		time.Sleep(time.Millisecond)
	}
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("destination", socket)
	prepared := map[string]int{}
	prepare := func(_ context.Context, p ipc.BackupRestorePreparePayload) (ipc.BackupRestorePrepareResult, error) {
		if p.SourceNodeID != "offline-source" || p.BackupNodeID != "backup" || p.DestNodeID != "destination" || p.DestHost != "fd00::3" {
			return ipc.BackupRestorePrepareResult{}, errors.New("prepare lost remote backup identity")
		}
		prepared[p.Dataset]++
		if path.Base(p.Dataset) == "cache" && prepared[p.Dataset] == 1 {
			return ipc.BackupRestorePrepareResult{}, errors.New("transfer interrupted")
		}
		return ipc.BackupRestorePrepareResult{Dataset: "floatlab/restores/backup/restore-op/" + p.MountID, Snapshot: p.Snapshot, GUID: "123"}, nil
	}
	w := New(db, store, pool, lifecycle, zap.NewNop())
	w.SetBackupRestorePreparer(prepare)
	raw, _ := json.Marshal(payload)
	if err := w.handleBackupRestore(ctx, raw); err == nil {
		t.Fatal("expected interrupted second mount")
	}
	state, _ := lifecycle.FSM().State(payload.StackID)
	if state.State != run.StateRestoring || activations["data"] != 1 || activations["cache"] != 0 {
		t.Fatalf("partial restore provisioned workload: state=%+v activations=%v", state, activations)
	}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE id='backup-restore-restore-op'`})
	if err != nil || len(rows.Values) != 1 {
		t.Fatalf("missing durable checkpoint: %+v %v", rows, err)
	}
	retryRaw := json.RawMessage(rows.Values[0][0].(string))
	var checkpoint BackupRestorePayload
	if err := json.Unmarshal(retryRaw, &checkpoint); err != nil {
		t.Fatal(err)
	}
	if !checkpoint.Prepared["floatlab/original/data"].Activated || len(checkpoint.Prepared) != 1 {
		t.Fatalf("activation checkpoint not durable: %+v", checkpoint.Prepared)
	}
	// Reconstruct the worker to model a process restart between mounts.
	restarted := New(db, store, pool, lifecycle, zap.NewNop())
	restarted.SetBackupRestorePreparer(prepare)
	if err := restarted.handleBackupRestore(ctx, retryRaw); err != nil {
		t.Fatal(err)
	}
	state, _ = lifecycle.FSM().State(payload.StackID)
	if state.State != run.StateProvisioning || prepared["floatlab/original/data"] != 1 || prepared["floatlab/original/cache"] != 2 || activations["data"] != 1 || activations["cache"] != 1 {
		t.Fatalf("retry repeated completed mount or provisioned early: state=%+v prepares=%v activations=%v", state, prepared, activations)
	}
	op, err := ops.Get(ctx, payload.OperationID)
	if err != nil || op.Checkpoint != "provisioning" {
		t.Fatalf("operation did not hand off to start: %+v %v", op, err)
	}
}
