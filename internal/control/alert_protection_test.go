package control

import (
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestScheduledReplicationNotifiesOnSecondOccurrence(t *testing.T) {
	ctx := t.Context()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := Server{db: db, store: config.NewStore(db)}
	resource := monitoring.Resource{HostID: "primary", WorkloadID: "app", Dataset: "floatlab/app/data", SourceID: "primary", DestinationID: "destination"}
	if err := s.store.PutBackupTarget(ctx, config.BackupTarget{StackID: resource.WorkloadID, NodeID: resource.DestinationID, Dataset: resource.Dataset, SourceNodeID: resource.SourceID, Schedule: "h/12"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Minute)
	occurrence := func(id, taskState string, due time.Time) {
		t.Helper()
		run := monitoring.Execution{ID: id, Kind: "replication", Resource: resource, Generation: 1, State: "scheduled", ScheduledAt: due}
		if err := monitoring.SaveExecution(ctx, db, run); err != nil {
			t.Fatal(err)
		}
		if taskState == "" {
			return
		}
		at := due.Add(time.Minute).Format(time.DateTime)
		if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO tasks(id,type,payload,state,attempts,created_at,started_at,updated_at) VALUES(?,'repl.trigger','{}',?,5,?,?,?)`, Params: []any{id, taskState, at, at, at}}}); err != nil {
			t.Fatal(err)
		}
	}
	check := func(at time.Time, incidents, notifications int, kind string) {
		t.Helper()
		if err := s.evaluateProtection(ctx, at); err != nil {
			t.Fatal(err)
		}
		rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT (SELECT COUNT(*) FROM alerts WHERE state='active'), (SELECT COUNT(*) FROM notifications), COALESCE((SELECT kind FROM notifications ORDER BY created_at DESC LIMIT 1),'')`})
		if err != nil {
			t.Fatal(err)
		}
		row := rows.Values[0]
		if number(row[0]) != incidents || number(row[1]) != notifications || str(row[2]) != kind {
			t.Fatalf("want %d active, %d notifications, latest %q, got %v", incidents, notifications, kind, row)
		}
	}
	occurrence("failed", "failed", now.Add(-4*time.Hour))
	check(now, 1, 0, "")
	check(now.Add(time.Second), 1, 0, "") // same occurrence after five attempts
	occurrence("missed", "", now.Add(-3*time.Hour))
	check(now.Add(2*time.Second), 1, 1, "alert")
	check(now.Add(3*time.Second), 1, 1, "alert")
	occurrence("success", "done", now.Add(-2*time.Hour))
	check(now.Add(4*time.Second), 0, 2, "alert.resolved")
	occurrence("failed-again", "failed", now.Add(-time.Hour))
	check(now.Add(5*time.Second), 1, 2, "alert.resolved")
}

func TestProtectionExecutionRetainsLateSuccessEvidence(t *testing.T) {
	due := time.Now().UTC().Truncate(time.Minute)
	run := monitoring.Execution{ID: "scheduled", State: "scheduled", ScheduledAt: due}
	started := due.Add(3 * time.Minute)
	run = reconcileProtectionExecution(run, "", time.Time{}, time.Time{}, started, 2*time.Minute, 5*time.Minute)
	if run.State != "missed" || !run.WasMissed {
		t.Fatalf("missing occurrence: %+v", run)
	}
	completed := due.Add(8 * time.Minute)
	run = reconcileProtectionExecution(run, "done", started, completed, completed, 2*time.Minute, 5*time.Minute)
	if run.State != "succeeded" || !run.WasMissed || !run.WasOverdue {
		t.Fatalf("late success lost its deadline evidence: %+v", run)
	}
}

func TestRecoveryPointAgeUsesVerifiedSourceTimePerDestination(t *testing.T) {
	ctx := t.Context()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := Server{db: db}
	target := config.BackupTarget{StackID: "app", Dataset: "floatlab/app/data", SourceNodeID: "source", NodeID: "backup", Generation: 1}
	now := time.Now().UTC().Truncate(time.Second)
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO monitoring_recovery_points VALUES(?,?,?,?,?,?,?,?)`, Params: []any{target.StackID, target.Dataset, target.SourceNodeID, target.NodeID, target.Generation, "snapshot", "guid", now.Add(-3 * time.Hour).Unix()}}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		destination, status, severity string
		at, deadline                  time.Time
	}{
		{"backup", "healthy", "warning", now.Add(-150 * time.Minute), time.Time{}},
		{"backup", "failing", "warning", now.Add(-70 * time.Minute), time.Time{}},
		{"backup", "failing", "critical", now, time.Time{}},
		{"another-backup", "unknown", "warning", now, now.Add(time.Minute)},
		{"another-backup", "failing", "critical", now, now.Add(-time.Minute)},
	} {
		target.NodeID = tc.destination
		transition := protectionTransition("replication.recovery_point.stale", "Recovery point", monitoring.Resource{}, tc.at)
		if err := s.recoveryPointTransition(ctx, &transition, target, time.Hour, 2*time.Hour, tc.deadline, tc.at); err != nil {
			t.Fatal(err)
		}
		if transition.Result.Status != tc.status || transition.Severity != tc.severity {
			t.Errorf("target %s at %s: got %s/%s, want %s/%s", tc.destination, tc.at, transition.Result.Status, transition.Severity, tc.status, tc.severity)
		}
	}
}
