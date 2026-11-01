package worker

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplicationRequiresSuccessfulSnapshotCreation(t *testing.T) {
	ctx := context.Background()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueTask(ctx, db, "snapshot", TaskSnapshotCreate, "app", SnapshotCreatePayload{}); err != nil {
		t.Fatal(err)
	}
	w := &Worker{db: db}
	raw, _ := json.Marshal(ReplTriggerPayload{SnapshotTaskID: "snapshot"})
	for _, state := range []string{"pending", "running", "failed"} {
		if err := db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE tasks SET state=? WHERE id='snapshot'`, Params: []interface{}{state}}}); err != nil {
			t.Fatal(err)
		}
		// No IPC pool: attempting replication before creation succeeds would panic.
		if err := w.handleReplTrigger(ctx, raw); err == nil || !strings.Contains(err.Error(), "has not succeeded") {
			t.Fatalf("state %s: %v", state, err)
		}
	}
}

func TestRemovedBackupSkipsQueuedWorkAndRetriesCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(db)
	target := config.BackupTarget{StackID: "app", Dataset: "floatlab/app/data", NodeID: "dest", SourceNodeID: "source", Schedule: "h/24", LocalPolicy: "h/24"}
	if err := store.PutBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveBackupTarget(ctx, "app", target.Dataset, "dest"); err != nil {
		t.Fatal(err)
	}
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	w := &Worker{db: db, store: store, pool: pool, log: zap.NewNop()}
	payload, _ := json.Marshal(ReplTriggerPayload{StackID: "app", NodeID: "source", Dataset: target.Dataset, DestNodeID: "dest"})
	if err := w.handleReplTrigger(ctx, payload); err != nil {
		t.Fatalf("disabled replication attempted IPC: %v", err)
	}
	if err := w.cleanupBackups(ctx); err == nil {
		t.Fatal("offline source cleanup reported success")
	}
	targets, err := store.BackupTargets(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if !targets[0].CleanupPending || targets[0].Error == "" {
		t.Fatalf("lost pending cleanup: %+v", targets)
	}
	socket := filepath.Join(t.TempDir(), "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	daemon.Handle("ping", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	created := 0
	daemon.Handle("fs.snapshot.create", func(_ context.Context, raw json.RawMessage) (any, error) {
		var request ipc.SnapshotCreatePayload
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		if request.Properties["floatlab:transfer-targets"] != "" || request.Properties["floatlab:transfer-generations"] != "" {
			return nil, errors.New("unsent snapshot pinned for offline target")
		}
		created++
		return nil, nil
	})
	daemon.Handle("fs.snapshot.prune", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	var cleanup map[string]interface{}
	daemon.Handle("fs.repl.cleanup", func(_ context.Context, raw json.RawMessage) (any, error) {
		if err := json.Unmarshal(raw, &cleanup); err != nil {
			return nil, err
		}
		return nil, nil
	})
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	defer func() { cancel(); <-done }()
	pool.Register("source", socket)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := pool.Execute(ctx, "source", "ping", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := w.cleanupBackups(ctx); err != nil {
		t.Fatal(err)
	}
	if cleanup["target_node_id"] != "dest" || cleanup["local_policy"] != "h/24" {
		t.Fatalf("wrong cleanup: %+v", cleanup)
	}
	targets, err = store.BackupTargets(ctx, "app")
	if err != nil || targets[0].CleanupPending || targets[0].Enabled || targets[0].Error != "" {
		t.Fatalf("cleanup state: %+v %v", targets, err)
	}
	if err := store.PutBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	// Old queued payloads must not reserve history, even after target removal/re-addition.
	for _, name := range []string{"fl-old-queued", "fl-new-queued"} {
		raw, _ := json.Marshal(map[string]interface{}{"dataset": target.Dataset, "node_id": "source", "stack_id": "app", "snap_type": "scheduled", "name": name, "transfer_targets": []string{"dest"}, "transfer_generations": map[string]int64{"dest": 1}})
		if err := w.handleSnapshotCreate(ctx, raw); err != nil {
			t.Fatal(err)
		}
	}
	if created != 2 {
		t.Fatalf("queued snapshots created=%d", created)
	}

}

func TestReplicationAlertsAreIndependentAndClearOnSuccess(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	w := &Worker{db: db}
	a := ReplTriggerPayload{StackID: "app", NodeID: "source", Dataset: "floatlab/app/data", DestNodeID: "a"}
	b := a
	b.DestNodeID = "b"
	for _, p := range []ReplTriggerPayload{a, a, b} {
		if err := w.replicationOutcome(ctx, p, errors.New("interrupted")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.replicationOutcome(ctx, a, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.backupOutcome(ctx, a, "backup_pruning", errors.New("held")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT kind,state FROM alerts ORDER BY id`})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.Values) != 3 {
		t.Fatalf("duplicate alerts: %+v", rows.Values)
	}
	counts := map[string]int{}
	for _, row := range rows.Values {
		counts[row[0].(string)+":"+row[1].(string)]++
	}
	if counts["replication:active"] != 1 || counts["replication:resolved"] != 1 || counts["backup_pruning:active"] != 1 {
		t.Fatalf("incorrect outcome: %+v", counts)
	}
}
