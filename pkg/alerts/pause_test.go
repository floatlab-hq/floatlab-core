package alerts

import (
	"testing"
	"time"
)

func TestPausePreservesIncidentAndRequiresNewEvidenceOnResume(t *testing.T) {
	rule := testRule()
	rule.PausedStacks = map[string]bool{"stopped": true}
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	old := State{AlertID: "incident", Labels: map[string]string{"stack_id": "stopped"}, PendingSince: now, RecoverySince: now, LastObserved: now}
	pausedAt := now.Add(time.Second)
	paused, ok := pause(rule, old, nil, pausedAt)
	if !ok || paused.Status != "paused" || paused.AlertID != old.AlertID || !paused.PendingSince.IsZero() || !paused.RecoverySince.IsZero() || !paused.ResumeAfter.Equal(pausedAt) {
		t.Fatalf("paused state: %+v", paused)
	}
	if _, ok := pause(rule, State{Labels: map[string]string{"stack_id": "running"}}, nil, pausedAt); ok {
		t.Fatal("running workload was paused")
	}
	rule.PausedStacks = nil
	stale := &Observation{Labels: paused.Labels, At: now, Recovered: true}
	resumed := advance(rule, paused, stale, now.Add(2*time.Second))
	if resumed.Status != "unknown" || resumed.AlertID != old.AlertID || !resumed.RecoverySince.IsZero() {
		t.Fatalf("pre-pause observation advanced rule: %+v", resumed)
	}
}
