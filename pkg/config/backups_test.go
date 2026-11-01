package config

import (
	"context"
	"errors"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"testing"
)

func TestBackupSourceChangeRequiresOldSourceCleanup(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	target := BackupTarget{StackID: "app", Dataset: "floatlab/app/data", NodeID: "backup", SourceNodeID: "source-a", Schedule: "h/24"}
	if err := store.PutBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	target.SourceNodeID = "source-b"
	if err := store.PutBackupTarget(ctx, target); !errors.Is(err, ErrBackupTargetConflict) {
		t.Fatalf("source changed without old hold cleanup: %v", err)
	}
	if err := store.RemoveBackupTarget(ctx, target.StackID, target.Dataset, target.NodeID); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBackupTarget(ctx, target); !errors.Is(err, ErrBackupTargetConflict) {
		t.Fatalf("source changed before pending cleanup finished: %v", err)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE backup_targets SET cleanup_pending=0 WHERE stack_id='app'`}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBackupTarget(ctx, target); err != nil {
		t.Fatalf("source change rejected after acknowledged cleanup: %v", err)
	}
	targets, err := store.BackupTargets(ctx, "app")
	if err != nil || len(targets) != 1 || targets[0].SourceNodeID != "source-b" || targets[0].Generation < 3 {
		t.Fatalf("source transition lost fencing: %+v %v", targets, err)
	}
}

func TestRegisteredNodeRemovalQueuesBackupCleanup(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	if err := store.CreateNode(ctx, &Node{ID: "backup", Name: "backup"}); err != nil {
		t.Fatal(err)
	}
	target := BackupTarget{StackID: "app", Dataset: "floatlab/app/data", NodeID: "backup", SourceNodeID: "source", Schedule: "h/24"}
	if err := store.PutBackupTarget(ctx, target); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteNode(ctx, "backup"); err != nil {
		t.Fatal(err)
	}
	targets, err := store.BackupTargets(ctx, "app")
	if err != nil || len(targets) != 1 || targets[0].Enabled || !targets[0].CleanupPending || targets[0].Generation != 2 {
		t.Fatalf("registered node deletion lost source cleanup: %+v %v", targets, err)
	}
	if err := store.PutBackupTarget(ctx, target); !errors.Is(err, ErrBackupTargetConflict) {
		t.Fatalf("removed registered node was reenabled: %v", err)
	}
}

func TestBackupTargetsPreserveCleanupAndAPIPolicies(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := NewStore(db)
	st := &Stack{ID: "app", Name: "demo", PrimaryNodeID: "source", ComposeYAML: `name: demo
services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: ./data
        target: /data
        x-fl-snapshots: h/24
        x-fl-backups:
          - node_id: compose-target
            schedule: h/24 d/28
`}
	if err := store.CreateStack(ctx, st); err != nil {
		t.Fatal(err)
	}
	api := BackupTarget{StackID: "app", Dataset: "floatlab/demo/data", NodeID: "api-target", SourceNodeID: "source", Schedule: "d/90"}
	if err := store.PutBackupTarget(ctx, api); err != nil {
		t.Fatal(err)
	}
	st.ComposeYAML = "name: demo\nservices:\n  web:\n    image: nginx\n    volumes:\n      - type: bind\n        source: ./data\n        target: /data\n        x-fl-snapshots: d/28\n"
	if err := store.UpdateStackCompose(ctx, st.ID, st.ComposeYAML); err != nil {
		t.Fatal(err)
	}
	targets, err := store.BackupTargets(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets: %+v", targets)
	}
	for _, target := range targets {
		if target.LocalPolicy != "1d/28" {
			t.Fatalf("stale source policy: %+v", target)
		}
		if target.NodeID == "api-target" && !target.Enabled {
			t.Fatal("compose update disabled API target")
		}
		if target.NodeID == "compose-target" && (target.Enabled || !target.CleanupPending) {
			t.Fatal("removed compose target lost durable cleanup")
		}
	}
	if err := store.DisableBackupNode(ctx, "api-target"); err != nil {
		t.Fatal(err)
	}
	if err := store.DisableBackupNode(ctx, "api-target"); err != nil {
		t.Fatal(err)
	}
	if err := store.PutBackupTarget(ctx, api); err == nil {
		t.Fatal("removed node was reconfigured")
	}
	targets, err = store.BackupTargets(ctx, "app")
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.Enabled || !target.CleanupPending {
			t.Fatalf("protection cleanup missing: %+v", target)
		}
	}
}
