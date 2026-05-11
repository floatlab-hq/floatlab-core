package run_test

import (
	"context"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestMachineHappyPath(t *testing.T) {
	m := run.New()
	inst := &run.StackInstance{ID: "test-1", State: run.StateIdle}

	steps := []struct {
		event run.Event
		want  run.State
	}{
		{run.EventStartStack, run.StateStarting},
		{run.EventStartDone, run.StateRunningPrimary},
		{run.EventFailoverStart, run.StateFailingOver},
		{run.EventFailoverDone, run.StateRunningBackup},
		{run.EventRestoreStart, run.StateRestoring},
		{run.EventRestoreDone, run.StateRunningPrimary},
		{run.EventStopStack, run.StateStopping},
		{run.EventStopDone, run.StateIdle},
	}

	for _, s := range steps {
		next, err := m.Apply(context.Background(), inst, s.event)
		if err != nil {
			t.Fatalf("event %s from %s: unexpected error: %v", s.event, inst.State, err)
		}
		if next.State != s.want {
			t.Fatalf("event %s from %s: got %s, want %s", s.event, inst.State, next.State, s.want)
		}
		inst = next
	}
}

func TestMachineInvalidTransition(t *testing.T) {
	m := run.New()
	inst := &run.StackInstance{ID: "test-2", State: run.StateIdle}
	_, err := m.Apply(context.Background(), inst, run.EventFailoverStart)
	if err == nil {
		t.Fatal("expected error for invalid transition")
	}
}

func TestMachineLockedTransitional(t *testing.T) {
	m := run.New()
	inst := &run.StackInstance{ID: "test-3", State: run.StateIdle}

	// Move into a transitional state (Starting)
	next, err := m.Apply(context.Background(), inst, run.EventStartStack)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.LockedBy == "" {
		t.Fatal("expected LockedBy to be set in transitional state")
	}

	// Attempting any event except StartDone/StartFailed should fail
	_, err = m.Apply(context.Background(), next, run.EventStopStack)
	if err == nil {
		t.Fatal("expected ErrTransitionInProgress when locked")
	}
}
