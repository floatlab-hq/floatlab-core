package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/stats"
)

type metricStub struct {
	rows  map[string][]stats.InstantSample
	err   error
	calls int
}

func (m *metricStub) QueryInstant(_ context.Context, query string, _ time.Time, _ time.Duration) ([]stats.InstantSample, error) {
	m.calls++
	if query == "source" && m.rows[query] == nil {
		var timestamps []stats.InstantSample
		for _, samples := range m.rows {
			for _, sample := range samples {
				sample.Value = float64(sample.ObservedAt.Unix())
				timestamps = append(timestamps, sample)
			}
		}
		return timestamps, m.err
	}
	return m.rows[query], m.err
}

func database(t *testing.T) *rqlite.Client {
	t.Helper()
	db := testdb.Start(t)
	if err := rqlite.Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := Migrate(context.Background(), db); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func rows(t *testing.T, db *rqlite.Client, query string) [][]interface{} {
	t.Helper()
	result, err := db.QueryStrong(context.Background(), rqlite.Statement{SQL: query})
	if err != nil {
		t.Fatal(err)
	}
	return result.Values
}

func TestPersistentPerResourceLifecycle(t *testing.T) {
	db := database(t)
	metrics := &metricStub{rows: map[string][]stats.InstantSample{}}
	broker := notify.NewBroker()
	events, cancel := broker.Subscribe()
	defer cancel()
	evaluator := Evaluator{DB: db, Metrics: metrics, Broker: broker, IsLeader: func() bool { return true }}
	rule := testRule()
	rule.For = 0
	now := time.Now().UTC().Truncate(time.Second)
	sample := func(device string, at time.Time) stats.InstantSample {
		return stats.InstantSample{Labels: map[string]string{"node_id": "node", "disk": device}, Value: 0, ObservedAt: at}
	}
	tick := func(at time.Time) {
		t.Helper()
		if err := evaluator.Tick(context.Background(), []Rule{rule}, at); err != nil {
			t.Fatal(err)
		}
	}
	metrics.rows[rule.Condition] = []stats.InstantSample{sample("a", now), sample("b", now)}
	tick(now)
	incidents := rows(t, db, `SELECT id,state FROM alerts ORDER BY id`)
	if len(incidents) != 2 || len(rows(t, db, `SELECT id FROM notifications`)) != 2 {
		t.Fatal(incidents)
	}
	if len(events) != 2 {
		t.Fatal("missing SSE", len(events))
	}
	tick(now.Add(30 * time.Second))
	if len(rows(t, db, `SELECT id FROM notifications`)) != 2 {
		t.Fatal("repeat notified")
	}
	metrics.err = errors.New("metrics unavailable")
	if err := evaluator.Tick(context.Background(), []Rule{rule}, now.Add(time.Minute)); err == nil {
		t.Fatal("expected query failure")
	}
	for _, incident := range rows(t, db, `SELECT state FROM alerts`) {
		if incident[0] != "active" {
			t.Fatal("query failure resolved incident")
		}
	}
	metrics.err = nil
	rule.Severity = "critical"
	metrics.rows[rule.Condition] = []stats.InstantSample{sample("b", now.Add(90*time.Second))}
	metrics.rows[rule.RecoveryCondition] = []stats.InstantSample{sample("a", now.Add(90*time.Second))}
	// A new evaluator instance models process/leadership replacement.
	evaluator = Evaluator{DB: db, Metrics: metrics, Broker: broker, IsLeader: func() bool { return true }}
	tick(now.Add(90 * time.Second))
	if len(rows(t, db, `SELECT id FROM alerts`)) != 2 {
		t.Fatal("restart duplicated incidents")
	}
	metrics.rows[rule.Condition] = []stats.InstantSample{sample("b", now.Add(150*time.Second))}
	metrics.rows[rule.RecoveryCondition] = []stats.InstantSample{sample("a", now.Add(150*time.Second))}
	tick(now.Add(150 * time.Second))
	if len(rows(t, db, `SELECT id FROM alerts WHERE state='resolved'`)) != 1 {
		t.Fatal("independent recovery failed")
	}
	if len(rows(t, db, `SELECT id FROM notifications WHERE kind='alert.resolved'`)) != 1 {
		t.Fatal("recovery notification missing")
	}
	metrics.rows[rule.Condition] = []stats.InstantSample{sample("a", now.Add(180*time.Second)), sample("b", now.Add(180*time.Second))}
	metrics.rows[rule.RecoveryCondition] = nil
	tick(now.Add(180 * time.Second))
	if len(rows(t, db, `SELECT id FROM alerts`)) != 3 {
		t.Fatal("recurrence overwrote history")
	}
	rule.Enabled = false
	tick(now.Add(210 * time.Second))
	if len(rows(t, db, `SELECT id FROM alerts WHERE state='active'`)) != 0 {
		t.Fatal("disabled rule retained active incident")
	}
	if len(rows(t, db, `SELECT id FROM alerts WHERE resolution_reason='rule disabled or removed'`)) != 2 {
		t.Fatal("closure reason missing")
	}
}

func TestAtomicTransitionAndCompetingWrites(t *testing.T) {
	db := database(t)
	ctx := context.Background()
	rule := testRule()
	rule.For = 0
	now := time.Now().UTC()
	observation := Observation{Labels: map[string]string{"disk": "a"}, Value: 99, At: now, Firing: true}
	next := advance(rule, State{}, &observation, now)
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `CREATE TRIGGER reject_notification BEFORE INSERT ON notifications BEGIN SELECT RAISE(ABORT,'notification unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	if err := save(ctx, db, nil, rule, storedState{}, next, now, ""); err == nil {
		t.Fatal("expected failed transaction")
	}
	if len(rows(t, db, `SELECT id FROM alerts`)) != 0 {
		t.Fatal("incident committed without notification")
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `DROP TRIGGER reject_notification`}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := save(ctx, db, nil, rule, storedState{}, next, now, ""); err != nil {
			t.Fatal(err)
		}
	}
	if len(rows(t, db, `SELECT id FROM alerts`)) != 1 || len(rows(t, db, `SELECT id FROM notifications`)) != 1 {
		t.Fatal("competing owner duplicated transition")
	}
}

func TestFollowerDoesNotEvaluate(t *testing.T) {
	metrics := &metricStub{}
	evaluator := Evaluator{Metrics: metrics, IsLeader: func() bool { return false }}
	if err := evaluator.Tick(context.Background(), []Rule{testRule()}, time.Now()); err != nil || metrics.calls != 0 {
		t.Fatal(err, metrics.calls)
	}
}

func TestDerivedResultsCannotRefreshFrozenSource(t *testing.T) {
	db := database(t)
	rule := testRule()
	now := time.Now().UTC().Truncate(time.Second)
	labels := map[string]string{"node_id": "node"}
	metrics := &metricStub{rows: map[string][]stats.InstantSample{"source": {{Labels: labels, Value: float64(now.Unix())}}}}
	evaluator := Evaluator{DB: db, Metrics: metrics, IsLeader: func() bool { return true }}
	for step := 0; step < 6; step++ {
		at := now.Add(time.Duration(step) * Interval)
		metrics.rows[rule.Condition] = []stats.InstantSample{{Labels: labels, Value: 95, ObservedAt: at}}
		if err := evaluator.Tick(context.Background(), []Rule{rule}, at); err != nil {
			t.Fatal(err)
		}
	}
	if len(rows(t, db, `SELECT id FROM alerts`)) != 0 {
		t.Fatal("frozen source fired despite fresh derived result timestamps")
	}
}

func TestComposeIncidentFollowsActiveNode(t *testing.T) {
	db := database(t)
	rule := testRule()
	rule.Compose = true
	rule.For = 0
	rule.StackID = "stack"
	rule.NodeID = "primary"
	now := time.Now().UTC().Truncate(time.Second)
	metrics := &metricStub{rows: map[string][]stats.InstantSample{}}
	evaluator := Evaluator{DB: db, Metrics: metrics, IsLeader: func() bool { return true }}
	for i, node := range []string{"primary", "backup"} {
		rule.NodeID = node
		at := now.Add(time.Duration(i) * Interval)
		metrics.rows[rule.Condition] = []stats.InstantSample{{Labels: map[string]string{"stack_id": "stack", "node_id": node, "service": "web", "replica": "1"}, Value: 95, ObservedAt: at}}
		if err := evaluator.Tick(context.Background(), []Rule{rule}, at); err != nil {
			t.Fatal(err)
		}
	}
	incidents := rows(t, db, `SELECT node_id FROM alerts`)
	if len(incidents) != 1 || incidents[0][0] != "backup" {
		t.Fatal("failover duplicated or stranded incident", incidents)
	}
}

type queryFunc func(context.Context, string, time.Time, time.Duration) ([]stats.InstantSample, error)

func (f queryFunc) QueryInstant(ctx context.Context, q string, at time.Time, age time.Duration) ([]stats.InstantSample, error) {
	return f(ctx, q, at, age)
}

func TestSlowRuleDoesNotStarveNextRule(t *testing.T) {
	db := database(t)
	now := time.Now().UTC().Truncate(time.Second)
	slow := testRule()
	slow.ID = "slow"
	slow.Condition = "slow"
	fast := testRule()
	fast.For = 0
	metrics := queryFunc(func(ctx context.Context, q string, _ time.Time, _ time.Duration) ([]stats.InstantSample, error) {
		if q == "slow" {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if q == fast.RecoveryCondition {
			return nil, nil
		}
		value := 95.0
		if q == "source" {
			value = float64(now.Unix())
		}
		return []stats.InstantSample{{Labels: map[string]string{"node_id": "node"}, Value: value}}, nil
	})
	evaluator := Evaluator{DB: db, Metrics: metrics, IsLeader: func() bool { return true }}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := evaluator.Tick(ctx, []Rule{slow, fast}, now); err == nil {
		t.Fatal("expected timeout")
	}
	if len(rows(t, db, `SELECT id FROM alerts WHERE rule_id='rule'`)) != 1 {
		t.Fatal("slow rule starved healthy rule")
	}
}
