package monitoring

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func database(t *testing.T) *rqlite.Client {
	t.Helper()
	db := testdb.Start(t)
	if err := rqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	return db
}

func count(t *testing.T, db *rqlite.Client, sql string) int {
	t.Helper()
	r, err := db.QueryStrong(context.Background(), rqlite.Statement{SQL: sql})
	if err != nil {
		t.Fatal(err)
	}
	return len(r.Values)
}

func TestAtomicConcurrentIncidentLifecycle(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	transition := Transition{Result: Result{CheckID: "host-api:one", Producer: "probe", Kind: "host-api", Resource: Resource{HostID: "one"}, Observer: "observer", Target: "10.0.0.1", Status: "failing", ObservedAt: now, EvaluatedAt: now}, RuleID: "host-api", Title: "Host API", Message: "unreachable", Severity: "warning", Action: "open", Notify: true}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `CREATE TRIGGER reject_notification BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT,'unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	if err := Record(ctx, db, nil, transition); err == nil {
		t.Fatal("expected notification failure")
	}
	if count(t, db, `SELECT id FROM alerts`) != 0 || count(t, db, `SELECT id FROM monitoring_observations`) != 0 {
		t.Fatal("partial incident transaction")
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `DROP TRIGGER reject_notification`}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Record(ctx, db, nil, transition); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if count(t, db, `SELECT id FROM alerts`) != 1 || count(t, db, `SELECT id FROM notifications`) != 1 || count(t, db, `SELECT id FROM monitoring_observations`) != 1 {
		t.Fatal("concurrent duplicate")
	}
	transition.Result.EvaluatedAt = now.Add(time.Minute)
	transition.Result.Status = "unknown"
	transition.Action = "keep"
	if err := Record(ctx, db, nil, transition); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT id FROM alerts WHERE state='active'`) != 1 {
		t.Fatal("unknown resolved incident")
	}
	transition.Result.Status = "healthy"
	transition.Action = "resolve"
	transition.Result.EvaluatedAt = now.Add(2 * time.Minute)
	transition.Result.ObservedAt = now.Add(time.Minute)
	transition.Result.ExpiresAt = now.Add(90 * time.Second)
	if err := Record(ctx, db, nil, transition); err == nil {
		t.Fatal("stale recovery accepted")
	}
	transition.Result.ExpiresAt = now.Add(3 * time.Minute)
	if err := Record(ctx, db, nil, transition); err != nil {
		t.Fatal(err)
	}
	transition.Result.Status = "failing"
	transition.Action = "open"
	transition.Result.ObservedAt = now.Add(3 * time.Minute)
	transition.Result.EvaluatedAt = transition.Result.ObservedAt
	transition.Result.ExpiresAt = now.Add(4 * time.Minute)
	if err := Record(ctx, db, nil, transition); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT id FROM alerts`) != 2 || count(t, db, `SELECT id FROM notifications`) != 3 {
		t.Fatal("recurrence overwrote history")
	}
	transition.Result.Producer = "other"
	transition.Result.EvaluatedAt = now.Add(4 * time.Minute)
	if err := Record(ctx, db, nil, transition); err == nil {
		t.Fatal("different producer claimed check")
	}
}

func TestExecutionLateSuccessAndSeparateAttempts(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	e := Execution{ID: "scrub:pool:occurrence", Kind: "scrub", Resource: Resource{HostID: "host", Pool: "pool-guid"}, State: "scheduled", ScheduledAt: now}
	if err := SaveExecution(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	e.State = "running"
	e.Active = true
	e.StartedAt = &now
	if err := SaveExecution(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"first", "second"} {
		if err := SaveAttempt(ctx, db, Attempt{ID: id, ExecutionID: e.ID, StartedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	completedAttempt := now.Add(time.Minute)
	a := Attempt{ID: "first", ExecutionID: e.ID, StartedAt: now, CompletedAt: &completedAttempt, Result: "failed"}
	for range 2 {
		if err := SaveAttempt(ctx, db, a); err != nil {
			t.Fatalf("completed attempt replay: %v", err)
		}
	}
	e.State = "overdue"
	if err := SaveExecution(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	completed := now.Add(time.Hour)
	e.State = "succeeded"
	e.Active = false
	e.CompletedAt = &completed
	if err := SaveExecution(ctx, db, e); err != nil {
		t.Fatal(err)
	}
	if count(t, db, `SELECT id FROM monitoring_executions`) != 1 || count(t, db, `SELECT id FROM monitoring_attempts`) != 2 {
		t.Fatal("attempts became occurrences")
	}
	if count(t, db, `SELECT id FROM monitoring_executions WHERE state='succeeded' AND json_extract(record_json,'$.was_overdue')=1`) != 1 {
		t.Fatal("late success erased overdue history")
	}
	if err := SaveExecution(ctx, db, e); err != nil {
		t.Fatalf("completed occurrence replay: %v", err)
	}
	e.State = "running"
	e.Active = true
	e.CompletedAt = nil
	if err := SaveExecution(ctx, db, e); err == nil {
		t.Fatal("completed occurrence restarted")
	}
	invalid := Execution{ID: "invalid", Kind: "scrub", State: "scheduled", ScheduledAt: now, CompletedAt: &completed}
	if err := SaveExecution(ctx, db, invalid); err == nil {
		t.Fatal("unstarted occurrence accepted completion time")
	}
}

func TestKeepCannotRecordStaleOrFutureSuccess(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	now := time.Now().UTC()
	transition := Transition{Result: Result{CheckID: "poll:one", Producer: "poll", Kind: "poll", Status: "healthy", ObservedAt: now, EvaluatedAt: now, ExpiresAt: now.Add(time.Minute)}, Severity: "warning", Action: "keep"}
	if err := Record(ctx, db, nil, transition); err != nil {
		t.Fatal(err)
	}
	transition.Result.EvaluatedAt = now.Add(2 * time.Minute)
	transition.Result.ObservedAt = now.Add(30 * time.Second)
	if err := Record(ctx, db, nil, transition); err == nil {
		t.Fatal("keep accepted stale healthy evidence")
	}
	transition.Result.ObservedAt = now.Add(3 * time.Minute)
	transition.Result.ExpiresAt = now.Add(4 * time.Minute)
	if err := Record(ctx, db, nil, transition); err == nil {
		t.Fatal("keep accepted future healthy evidence")
	}
	stored, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT state_json FROM monitoring_checks WHERE id='poll:one'`})
	if err != nil {
		t.Fatal(err)
	}
	var check Check
	if err := json.Unmarshal([]byte(stored.Values[0][0].(string)), &check); err != nil {
		t.Fatal(err)
	}
	if !check.LastSuccess.Equal(now) {
		t.Fatalf("invalid evidence advanced last success: %s", check.LastSuccess)
	}
}
