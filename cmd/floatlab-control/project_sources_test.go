package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestProjectCutoverPreservesBothSourcesAndExportsOnlyPeerCopy(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	db, err := rqlite.OpenLocal(filepath.Join(dir, "node.sqlite"), "node")
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
	project := filepath.Join(dir, "stack")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "docker-compose.yml")
	legacyMirror := "name: demo\nservices: {web: {image: nginx:1}}\n"
	legacyAuthority := "name: demo\nservices: {web: {image: nginx:2}}\n"
	manualEdit := "name: demo\nservices: {web: {image: nginx:3}}\n"
	if err := os.WriteFile(path, []byte(legacyMirror), 0600); err != nil {
		t.Fatal(err)
	}
	stack := &config.Stack{ID: "stack", Name: "demo", PrimaryNodeID: "node", ZFSDataset: strings.TrimPrefix(project, "/"), ComposeYAML: legacyAuthority}
	stacks := config.NewProjectStore(db, nil, readProjectCompose, func(_ context.Context, source *config.Stack) error {
		return update.ReplaceFile(path, []byte(source.ComposeYAML), 0600)
	})
	if err := stacks.CreateStack(ctx, stack); err != nil {
		t.Fatal(err)
	}
	if err := migrateProjectSources(ctx, stacks, "node"); err != nil {
		t.Fatal(err)
	}
	backup, err := filepath.Glob(filepath.Join(project, compose.ProjectStateDir, "compose-before-cutover-*.yml"))
	if err != nil || len(backup) != 1 {
		t.Fatalf("cutover backups: %v, %v", backup, err)
	}
	if saved, err := os.ReadFile(backup[0]); err != nil || string(saved) != legacyMirror {
		t.Fatalf("previous file was not preserved: %q, %v", saved, err)
	}
	if saved, err := os.ReadFile(path); err != nil || string(saved) != legacyAuthority {
		t.Fatalf("legacy authority did not move to disk: %q, %v", saved, err)
	}
	if err := update.ReplaceFile(path, []byte(manualEdit), 0600); err != nil {
		t.Fatal(err)
	}
	replicaPath, cleanup, err := db.ExportReplica(ctx, dir, projectReplicaStatements)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	peerID, err := rqlite.ReadReplicaIdentity(ctx, replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.LocalIdentity(ctx)
	if err != nil || id != peerID {
		t.Fatalf("peer export changed database identity: %+v %+v %v", id, peerID, err)
	}
	replica, err := rqlite.OpenReplica(replicaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	if remote, err := config.NewStore(replica).GetStack(ctx, "stack"); err != nil || remote.ComposeYAML != manualEdit {
		t.Fatalf("peer did not receive editable file: %+v, %v", remote, err)
	}
	if local, err := stacks.CatalogStack(ctx, "stack"); err != nil || !local.ComposeFile || local.ComposeYAML != "" {
		t.Fatalf("node catalog retained authoritative YAML: %+v, %v", local, err)
	}
}
