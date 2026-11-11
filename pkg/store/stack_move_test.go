package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStackMoveActivationPreservesSnapshotAndRejectsForeignTarget(t *testing.T) {
	dir, put := restoreFixture(t)
	ctx := context.Background()
	root := "floatlab/web"
	mount := root + "/data"
	snapshot := "fl-000000"
	datasets := []DatasetParams{{Dataset: root}, {Dataset: mount, BlockSize: "32K", Compression: "lz4", Quota: "128M"}}
	for _, dataset := range datasets {
		backup := MoveBackupDataset("source", root, "stack", dataset.Dataset)
		put(backup, map[string]string{"floatlab:backup-source": "source", "snapshots": backup + "@" + snapshot + "\t42\n"})
	}
	put(root, map[string]string{"floatlab:move-operation": "other"})
	if err := activateStackMoveLocked(ctx, "move", "stack", "source", root, snapshot, datasets); err == nil {
		t.Fatal("accepted a foreign destination")
	}
	if err := os.RemoveAll(filepath.Join(dir, root)); err != nil {
		t.Fatal(err)
	}
	backupMount := MoveBackupDataset("source", root, "stack", mount)
	put(backupMount, map[string]string{"receive_resume_token": "partial"})
	if err := activateStackMoveLocked(ctx, "move", "stack", "source", root, snapshot, datasets); err == nil {
		t.Fatal("activated an incomplete receive")
	}
	if _, err := os.Stat(filepath.Join(dir, root)); !os.IsNotExist(err) {
		t.Fatalf("activation changed the destination before validating all datasets: %v", err)
	}
	put(backupMount, map[string]string{"receive_resume_token": "-"})
	t.Setenv("RESTORE_TEST_FAIL_MOUNT", root)
	if err := activateStackMoveLocked(ctx, "move", "stack", "source", root, snapshot, datasets); err == nil {
		t.Fatal("activation succeeded despite an interrupted mount")
	}
	if _, err := os.Stat(filepath.Join(dir, root)); err != nil {
		t.Fatalf("interruption did not leave the renamed root for recovery: %v", err)
	}
	t.Setenv("RESTORE_TEST_FAIL_MOUNT", "")
	for range 2 {
		if err := activateStackMoveLocked(ctx, "move", "stack", "source", root, snapshot, datasets); err != nil {
			t.Fatal(err)
		}
	}
	for dataset, properties := range map[string]map[string]string{
		root:  {"mounted": "yes", "mountpoint": "/" + root, "readonly": "off"},
		mount: {"mounted": "yes", "mountpoint": "/" + mount, "readonly": "off", "quota": "128M", "recordsize": "32K", "compression": "lz4"},
	} {
		for name, expected := range properties {
			value, err := os.ReadFile(filepath.Join(dir, dataset, name))
			if err != nil || string(value) != expected {
				t.Fatalf("%s %s=%q, want %q: %v", dataset, name, value, expected, err)
			}
		}
		snapshots, err := os.ReadFile(filepath.Join(dir, dataset, "snapshots"))
		if err != nil || !strings.Contains(string(snapshots), dataset+"@"+snapshot+"\t42") {
			t.Fatalf("missing relocated snapshot for %s: %s %v", dataset, snapshots, err)
		}
	}
}

func TestStackMoveArchiveRetainsDataAndRejectsUnmanagedChildren(t *testing.T) {
	dir, put := restoreFixture(t)
	ctx := context.Background()
	root := "floatlab/web"
	mount := root + "/data"
	snapshot := "fl-000000"
	datasets := []DatasetParams{{Dataset: root}, {Dataset: mount}}
	for _, dataset := range []string{root, mount, root + "/unmanaged"} {
		put(dataset, map[string]string{"mounted": "yes", "snapshots": dataset + "@" + snapshot + "\t42\n", "payload": dataset})
	}
	if err := archiveStackMoveLocked(ctx, "move", root, snapshot, datasets); err == nil {
		t.Fatal("archived an unmanaged child")
	}
	if err := os.RemoveAll(filepath.Join(dir, root, "unmanaged")); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := archiveStackMoveLocked(ctx, "move", root, snapshot, datasets); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, root)); !os.IsNotExist(err) {
		t.Fatalf("old workload path still exists: %v", err)
	}
	archive := "floatlab/restores/moves/move"
	for suffix, expected := range map[string]string{"/payload": root, "/data/payload": mount, "/data/readonly": "on", "/data/mounted": "no"} {
		got, err := os.ReadFile(filepath.Join(dir, archive+suffix))
		if err != nil || string(got) != expected {
			t.Fatalf("archive %s=%q, want %q: %v", suffix, got, expected, err)
		}
	}
}

func TestStackMoveRejectsUnsafeDatasetPaths(t *testing.T) {
	for _, root := range []string{"floatlab/system", "floatlab/restores", "floatlab/backups", "other/app", "floatlab/app/data"} {
		datasets := []DatasetParams{{Dataset: root}}
		if err := validateStackMove("move", root, "fl-000000", datasets); err == nil {
			t.Fatalf("accepted unsafe root %q", root)
		}
	}
	for _, child := range []string{"floatlab/other/data", "floatlab/web/data/nested", "floatlab/web/..", "floatlab/web"} {
		datasets := []DatasetParams{{Dataset: "floatlab/web"}, {Dataset: child}}
		if err := validateStackMove("move", "floatlab/web", "fl-000000", datasets); err == nil {
			t.Fatalf("accepted unsafe mount %q", child)
		}
	}
}
