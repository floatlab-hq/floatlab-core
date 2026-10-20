package alerts

import (
	"testing"
	"time"
)

func testRule() Rule {
	return Rule{ID: "rule", Name: "High usage", Condition: "usage > 90", RecoveryCondition: "usage < 85", FreshnessQuery: "source", Severity: "warning", For: time.Minute, RecoveryFor: time.Minute, MaxAge: DefaultMaxAge, Enabled: true}
}

func TestSustainedConditionRequiresNewFreshEvidence(t *testing.T) {
	rule := testRule()
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	observation := Observation{Labels: map[string]string{"node_id": "node"}, Value: 95, At: now, Firing: true}
	state := advance(rule, State{}, &observation, now)
	if state.Status != "pending" {
		t.Fatal(state)
	}
	state = advance(rule, state, &observation, now.Add(time.Minute))
	if state.Status != "pending" {
		t.Fatal("same sample advanced timer", state)
	}
	observation.At = now.Add(time.Minute)
	state = advance(rule, state, &observation, observation.At)
	if state.Status != "firing" {
		t.Fatal(state)
	}
	state.AlertID = "incident"
	state = advance(rule, state, nil, now.Add(90*time.Second))
	if state.Status != "unknown" || state.AlertID != "incident" {
		t.Fatal("missing data cleared incident", state)
	}
	observation.Firing, observation.Recovered, observation.Value = false, true, 80
	observation.At = now.Add(2 * time.Minute)
	state = advance(rule, state, &observation, observation.At)
	if state.Status != "firing" {
		t.Fatal("recovery duration skipped", state)
	}
	observation.At = now.Add(3 * time.Minute)
	state = advance(rule, state, &observation, observation.At)
	if state.Status != "resolved" {
		t.Fatal(state)
	}
}

func TestUnknownGapAndRuleEditResetPending(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(map[bool]string{false: "gap", true: "edit"}[edit], func(t *testing.T) {
			rule := testRule()
			now := time.Now().UTC()
			observation := Observation{Value: 95, At: now, Firing: true}
			state := advance(rule, State{}, &observation, now)
			if edit {
				rule.Condition = "usage > 94"
				observation.At = now.Add(time.Minute)
			} else {
				observation.At = now.Add(4 * time.Minute)
			}
			state = advance(rule, state, &observation, observation.At)
			if state.Status != "pending" || !state.PendingSince.Equal(observation.At) {
				t.Fatal(state)
			}
		})
	}
}

func TestInvalidAndConflictingEvidenceIsUnknown(t *testing.T) {
	rule := testRule()
	now := time.Now().UTC()
	for name, observation := range map[string]Observation{
		"old":      {At: now.Add(-3 * time.Minute), Firing: true},
		"future":   {At: now.Add(time.Second), Firing: true},
		"conflict": {At: now, Firing: true, Recovered: true},
	} {
		t.Run(name, func(t *testing.T) {
			if state := advance(rule, State{AlertID: "active"}, &observation, now); state.Status != "unknown" || state.AlertID != "active" {
				t.Fatal(state)
			}
		})
	}
}
