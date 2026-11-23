package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestComposeAuthorityMovesFromDraftToProject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := rqlite.OpenLocal(filepath.Join(dir, "node.sqlite"), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "compose.yml")
	writeFailure := errors.New("dataset unavailable")
	failWrite := true
	project := NewProjectStore(db, nil, func(context.Context, *Stack) (string, error) {
		data, err := os.ReadFile(path)
		return string(data), err
	}, func(_ context.Context, stack *Stack) error {
		if failWrite {
			return writeFailure
		}
		return os.WriteFile(path, []byte(stack.ComposeYAML), 0600)
	})
	const source = `name: demo
x-fl-stack:
  primary_node: ${NODE:-local}
  failover:
    mode: ${MODE:-manual}
    auto_trigger_after: ${AFTER:-120s}
services:
  web:
    image: nginx
`
	stack := &Stack{ID: "stack", Name: "demo", PrimaryNodeID: "local", ZFSDataset: "floatlab/demo", ComposeYAML: source}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `ALTER TABLE stacks DROP COLUMN compose_file`}}); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(db).CreateStack(ctx, stack); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	pending, err := project.PendingStackCompose(ctx)
	if err != nil || len(pending) != 1 || pending[0].ComposeYAML != source {
		t.Fatalf("migration draft: %+v, %v", pending, err)
	}
	if err := project.PublishStackCompose(ctx, stack); !errors.Is(err, writeFailure) {
		t.Fatalf("publish error: %v", err)
	}
	if draft, err := project.GetStack(ctx, stack.ID); err != nil || draft.ComposeFile || draft.ComposeYAML != source {
		t.Fatalf("failed write discarded draft: %+v, %v", draft, err)
	}
	failWrite = false
	if err := project.PublishStackCompose(ctx, stack); err != nil {
		t.Fatal(err)
	}
	assertNoSQLSource := func() {
		t.Helper()
		catalog, err := project.CatalogStack(ctx, stack.ID)
		if err != nil || !catalog.ComposeFile || catalog.ComposeYAML != "" {
			t.Fatalf("published catalog: %+v, %v", catalog, err)
		}
	}
	assertNoSQLSource()
	reader := NewProjectStore(db, nil, project.readCompose, nil)
	if got, err := reader.GetStack(ctx, stack.ID); err != nil || got.ComposeYAML != source || got.FailoverMode != "manual" || got.AutoTriggerAfter != "120s" {
		t.Fatalf("read-only project source: %+v, %v", got, err)
	}
	if err := reader.UpdateStackCompose(ctx, stack.ID, source); err == nil {
		t.Fatal("read-only project store accepted a write")
	}
	version, err := db.Query(ctx, rqlite.Statement{SQL: `PRAGMA user_version`})
	if err != nil || len(version.Values) != 1 || version.Values[0][0] != float64(rqlite.ProjectLocalSchemaVersion) {
		t.Fatalf("downgrade fence missing: %+v, %v", version, err)
	}
	edited := strings.NewReplacer("${MODE:-manual}", "auto", "${AFTER:-120s}", "20s").Replace(source)
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if list, err := project.ListStacks(ctx); err != nil || len(list) != 1 || list[0].ComposeYAML != edited || list[0].FailoverMode != "auto" || list[0].AutoTriggerAfter != "20s" {
		t.Fatalf("file edit was ignored: %+v, %v", list, err)
	}
	if err := project.UpdateStackCompose(ctx, stack.ID, strings.Replace(edited, "${NODE:-local}", "elsewhere", 1)); err == nil {
		t.Fatal("file update changed the registered owner")
	}
	const broken = "services: ["
	if err := os.WriteFile(path, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := project.ReadStackCompose(ctx, stack.ID); err != nil || got != broken {
		t.Fatalf("raw access to invalid source: %q, %v", got, err)
	}
	if _, err := project.GetStack(ctx, stack.ID); err == nil {
		t.Fatal("invalid source used as a runnable stack")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := project.GetStack(ctx, stack.ID); !errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrStackNotFound) {
		t.Fatalf("missing authoritative file error: %v", err)
	}
	if err := project.UpdateStackCompose(ctx, stack.ID, source); err != nil {
		t.Fatalf("repair missing published file: %v", err)
	}
	if repaired, err := os.ReadFile(path); err != nil || string(repaired) != source {
		t.Fatalf("repaired source: %q, %v", repaired, err)
	}

	secondarySource := strings.Replace(source, "  failover:", "  secondary_node: peer\n  failover:", 1)
	failWrite = true
	if err := project.UpdateStackComposeAndSecondary(ctx, stack.ID, secondarySource, "peer"); !errors.Is(err, writeFailure) {
		t.Fatalf("secondary publication error: %v", err)
	}
	if prior, err := project.GetStack(ctx, stack.ID); err != nil || prior.ComposeYAML != source {
		t.Fatalf("reserved placement made previous source unreadable: %+v, %v", prior, err)
	}
	failWrite = false
	if err := project.UpdateStackCompose(ctx, stack.ID, secondarySource); err != nil {
		t.Fatalf("finish secondary publication: %v", err)
	}
	if got, err := project.GetStack(ctx, stack.ID); err != nil || got.BackupNodeID != "peer" || got.ComposeYAML != secondarySource {
		t.Fatalf("secondary assignment did not publish authoritative source: %+v, %v", got, err)
	}
	if err := project.UpdateStackComposeAndSecondary(ctx, stack.ID, strings.Replace(secondarySource, "peer", "other", 1), "other"); err == nil {
		t.Fatal("established secondary assignment was overwritten")
	}

	assertNoSQLSource()
}
