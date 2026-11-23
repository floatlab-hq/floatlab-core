package control

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestManagementOperationFailureTracksVerifiedRetry(t *testing.T) {
	ctx := t.Context()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "operations.sqlite"), "local")
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
	s := Server{db: db, cfg: &Config{LocalNodeID: "local"}}
	store := operation.NewStore(db)
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	insert := func(id, action, path, state, checkpoint string, httpStatus int) {
		t.Helper()
		if err := store.Create(ctx, operation.Operation{ID: id, StackID: "stack", Action: action, State: state, Checkpoint: checkpoint, CreatedAt: now.Add(-time.Second), UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := db.Execute(ctx, []rqlite.Statement{
			{SQL: `INSERT INTO idempotency_keys(actor,key,method,path,request_hash,operation_id,status,created_at) VALUES('user',?,'POST',?,'hash',?,?,?)`, Params: []any{id, path, id, httpStatus, now}},
			{SQL: `UPDATE operations SET error='do-not-publish-credentials' WHERE id=? AND state='failed'`, Params: []any{id}},
		}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	tick := func() {
		t.Helper()
		if err := s.evaluateOperationFailures(ctx, now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	query := func(sql string) [][]interface{} {
		t.Helper()
		result, err := db.QueryStrong(ctx, rqlite.Statement{SQL: sql})
		if err != nil {
			t.Fatal(err)
		}
		return result.Values
	}
	check := func(active, resolved, notifications int) {
		t.Helper()
		if actual := len(query(`SELECT id FROM alerts WHERE state='active' AND severity='critical' AND rule_id='management.operation.failed'`)); actual != active {
			t.Fatalf("got %d active incidents, want %d", actual, active)
		}
		if actual := len(query(`SELECT id FROM alerts WHERE state='resolved' AND rule_id='management.operation.failed'`)); actual != resolved {
			t.Fatalf("got %d resolved incidents, want %d", actual, resolved)
		}
		if actual := len(query(`SELECT id FROM notifications`)); actual != notifications {
			t.Fatalf("got %d notifications, want %d", actual, notifications)
		}
	}

	insert("failed-backup-restore", "start", "/api/v1/storage/backups/restore", "failed", "restoring-data", 202)
	tick()
	check(1, 0, 1)
	message := str(query(`SELECT body FROM notifications`)[0][0])
	if !strings.Contains(message, "restoring-data") || !strings.Contains(message, "failed-backup-restore") || strings.Contains(message, "do-not-publish-credentials") {
		t.Fatalf("invalid notification evidence: %s", message)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE notifications SET state='read'`}}); err != nil {
		t.Fatal(err)
	}
	insert("successful-start", "start", "/api/v1/stacks/stack/start", "succeeded", "succeeded", 202)
	insert("rejected-restore", "start", "/api/v1/storage/backups/restore", "failed", "failed", 409)
	tick()
	check(1, 0, 1)
	s = Server{db: db, cfg: s.cfg}
	tick()
	check(1, 0, 1)

	insert("again-failed", "start", "/api/v1/storage/backups/restore", "failed", "do-not-publish-checkpoint", 202)
	tick()
	check(1, 0, 1)
	if message := str(query(`SELECT message FROM alerts`)[0][0]); !strings.Contains(message, "again-failed") || strings.Contains(message, "do-not-publish-checkpoint") {
		t.Fatalf("unexpected refreshed incident evidence: %s", message)
	}
	insert("verified-restore", "start", "/api/v1/storage/backups/restore", "succeeded", "succeeded", 202)
	tick()
	check(0, 1, 2)

	insert("later-failure", "start", "/api/v1/storage/backups/restore", "failed", "failed", 202)
	tick()
	check(1, 1, 3)
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO alert_rule_overrides(id,overrides,updated_at) VALUES('management.operation.failed','{"enabled":false}',?)`, Params: []any{now}}}); err != nil {
		t.Fatal(err)
	}
	tick()
	check(0, 2, 3)
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `DELETE FROM alert_rule_overrides WHERE id='management.operation.failed'`}}); err != nil {
		t.Fatal(err)
	}
	tick()
	check(1, 2, 4)
}
