package raft

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/run"
	hraft "github.com/hashicorp/raft"
)

func applyFSMEntry(t *testing.T, f *FSM, entry run.StackStateChanged) error {
	t.Helper()
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	if result := f.Apply(&hraft.Log{Data: b}); result != nil {
		return result.(error)
	}
	return nil
}

func TestFSMRejectsStaleStateAndGeneration(t *testing.T) {
	for _, test := range []struct {
		name    string
		current run.State
	}{
		{"different state", run.StateStopping},
		{"same state in later operation", run.StateStarting},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := NewFSM()
			observed := time.Unix(100, 0).UTC()
			current := observed.Add(time.Second)
			if err := applyFSMEntry(t, f, run.StackStateChanged{StackID: "web", To: test.current, Timestamp: current}); err != nil {
				t.Fatal(err)
			}
			events, unsubscribe := f.Subscribe()
			defer unsubscribe()
			err := applyFSMEntry(t, f, run.StackStateChanged{StackID: "web", From: run.StateStarting, To: run.StateRunningPrimary, Event: run.EventStartDone, ExpectedUpdatedAt: observed, Timestamp: current.Add(time.Second)})
			if !errors.Is(err, run.ErrInvalidTransition) {
				t.Fatalf("stale completion accepted: %v", err)
			}
			state, _ := f.State("web")
			if state.State != test.current || !state.UpdatedAt.Equal(current) {
				t.Fatalf("rejected completion changed state: %+v", state)
			}
			select {
			case event := <-events:
				t.Fatalf("rejected completion emitted: %+v", event)
			default:
			}
		})
	}
}

func TestFSMConditionalCreationAndLegacySnapshot(t *testing.T) {
	f := NewFSM()
	created := time.Unix(100, 0).UTC()
	entry := run.StackStateChanged{StackID: "web", From: run.StateIdle, To: run.StateProvisioning, Event: run.EventCreateStack, Timestamp: created}
	if err := applyFSMEntry(t, f, entry); err != nil {
		t.Fatal(err)
	}
	if err := applyFSMEntry(t, f, entry); !errors.Is(err, run.ErrInvalidTransition) {
		t.Fatalf("duplicate creation accepted: %v", err)
	}
	// Existing import data stores final states rather than replayable commands.
	if err := applyFSMEntry(t, f, run.StackStateChanged{StackID: "imported", To: run.StateRunningPrimary, Timestamp: created}); err != nil {
		t.Fatal(err)
	}
	// A completion without an origin must not be treated as an import.
	if err := applyFSMEntry(t, f, run.StackStateChanged{StackID: "imported", To: run.StateIdle, Event: run.EventStopDone}); !errors.Is(err, run.ErrInvalidTransition) {
		t.Fatalf("eventful snapshot accepted: %v", err)
	}
}
