package alerts

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/stats"
)

func TestCriticalFiresBeforeWarningAndDowngradesWithFreshEvidence(t *testing.T) {
	rule := testRule()
	rule.Bands = &Bands{
		Warning:  Band{Trigger: 90, Recovery: 85, For: time.Minute, RecoveryFor: time.Minute},
		Critical: Band{Trigger: 97, Recovery: 92, For: 0, RecoveryFor: 30 * time.Second},
	}
	now := time.Unix(1800000000, 0).UTC()
	state := State{}
	tick := func(value float64) {
		state = advance(rule, state, &Observation{Value: value, At: now, Firing: true}, now)
		now = now.Add(30 * time.Second)
	}
	tick(98)
	if state.Status != "firing" || state.Severity != "critical" {
		t.Fatal(state)
	}
	state.AlertID = "incident"
	tick(91)
	if state.Severity != "critical" {
		t.Fatal(state)
	}
	tick(91)
	if state.Status != "firing" || state.Severity != "warning" || state.AlertID != "incident" {
		t.Fatal(state)
	}
	tick(80)
	state = advance(rule, state, nil, now)
	now = now.Add(30 * time.Second)
	tick(80)
	tick(80)
	if state.Status != "firing" {
		t.Fatal(state)
	}
	tick(80)
	if state.Status != "resolved" {
		t.Fatal(state)
	}
}

func TestBandedSeverityPersistsWithoutDuplicatingIncidents(t *testing.T) {
	ctx := t.Context()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "alerts.sqlite"), "local")
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
	metrics := &metricStub{rows: map[string][]stats.InstantSample{}}
	evaluator := Evaluator{DB: db, Metrics: metrics, Broker: notify.NewBroker(), IsLeader: func() bool { return true }}
	rule := testRule()
	rule.Bands = &Bands{
		Query: "utilization", Unit: "percent",
		Warning:  Band{Trigger: 90, Recovery: 85},
		Critical: Band{Trigger: 97, Recovery: 92},
	}
	now := time.Unix(1800000000, 0).UTC()
	var incidentID interface{}
	for _, tick := range []struct {
		value             float64
		severity, state   string
		notificationCount int
	}{
		{91, "warning", "active", 1},
		{98, "critical", "active", 2},
		{91, "warning", "active", 2},
		{80, "warning", "resolved", 3},
	} {
		metrics.rows["utilization"] = []stats.InstantSample{{Labels: map[string]string{"node_id": "node"}, Value: tick.value, ObservedAt: now}}
		if err := evaluator.Tick(ctx, []Rule{rule}, now); err != nil {
			t.Fatal(err)
		}
		got := rows(t, db, `SELECT id,severity,state FROM alerts`)
		if len(got) != 1 || got[0][1] != tick.severity || got[0][2] != tick.state {
			t.Fatalf("observation %g: unexpected incident %v", tick.value, got)
		}
		if incidentID != nil && incidentID != got[0][0] {
			t.Fatalf("observation %g: incident changed identity", tick.value)
		}
		incidentID = got[0][0]
		if count := len(rows(t, db, `SELECT id FROM notifications`)); count != tick.notificationCount {
			t.Fatalf("observation %g: got %d notifications, expected %d", tick.value, count, tick.notificationCount)
		}
		now = now.Add(30 * time.Second)
	}
}
