package raft

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/pkg/run"
	hraft "github.com/hashicorp/raft"
)

// FSM applies StackStateChanged log entries from the Raft log to the in-memory
// state map. It is pure: no I/O, no side effects.
type FSM struct {
	mu     sync.RWMutex
	stacks map[string]*run.StackInstance

	subsMu sync.Mutex
	subs   map[uint64]chan run.StackStateChanged
	nextID uint64
}

func NewFSM() *FSM {
	return &FSM{
		stacks: make(map[string]*run.StackInstance),
		subs:   make(map[uint64]chan run.StackStateChanged),
	}
}

// Subscribe returns a buffered channel that receives each applied
// StackStateChanged entry and an unsubscribe function the caller must invoke
// when done. Multiple callers each get their own independent channel.
func (f *FSM) Subscribe() (<-chan run.StackStateChanged, func()) {
	f.subsMu.Lock()
	id := f.nextID
	f.nextID++
	ch := make(chan run.StackStateChanged, 64)
	f.subs[id] = ch
	f.subsMu.Unlock()

	return ch, func() {
		f.subsMu.Lock()
		delete(f.subs, id)
		close(ch)
		f.subsMu.Unlock()
	}
}

func (f *FSM) fanOut(entry run.StackStateChanged) {
	f.subsMu.Lock()
	defer f.subsMu.Unlock()
	for _, ch := range f.subs {
		select {
		case ch <- entry:
		default: // slow subscriber; drop
		}
	}
}

func (f *FSM) Apply(l *hraft.Log) interface{} {
	var entry run.StackStateChanged
	if err := json.Unmarshal(l.Data, &entry); err != nil {
		return fmt.Errorf("fsm: unmarshal: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.validateLocked(entry); err != nil {
		return err
	}
	f.applyLocked(entry)
	return nil
}

func (f *FSM) validateLocked(entry run.StackStateChanged) error {
	// Eventless entries without an origin are legacy snapshots used by imports.
	if entry.Event == "" && entry.From == "" && entry.ExpectedUpdatedAt.IsZero() {
		return nil
	}
	state, updatedAt := run.StateIdle, time.Time{}
	if inst, ok := f.stacks[entry.StackID]; ok {
		state, updatedAt = inst.State, inst.UpdatedAt
	}
	// Old restore tasks omitted From on their initial Restoring entry.
	if _, exists := f.stacks[entry.StackID]; !exists && entry.From == "" && entry.Event == run.EventBackupRestoreStart {
		return nil
	}
	if state != entry.From || (!entry.ExpectedUpdatedAt.IsZero() && !updatedAt.Equal(entry.ExpectedUpdatedAt)) {
		return fmt.Errorf("%w: stack %s changed since %s was observed", run.ErrInvalidTransition, entry.StackID, entry.From)
	}
	return nil
}

func (f *FSM) applyLocked(entry run.StackStateChanged) {

	inst, ok := f.stacks[entry.StackID]
	if !ok {
		inst = &run.StackInstance{ID: entry.StackID}
	}
	updated := *inst
	updated.PreviousState = updated.State
	if !ok {
		updated.PreviousState = entry.From
	}
	updated.State = entry.To
	if entry.NodeID != "" {
		updated.NodeID = entry.NodeID
	}
	updated.UpdatedAt = entry.Timestamp
	f.stacks[entry.StackID] = &updated

	f.fanOut(entry)
}

func (f *FSM) Snapshot() (hraft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	snapshot := make(map[string]*run.StackInstance, len(f.stacks))
	for k, v := range f.stacks {
		cp := *v
		snapshot[k] = &cp
	}
	return &fsmSnapshot{stacks: snapshot}, nil
}

func (f *FSM) Restore(rc io.ReadCloser) error {
	defer rc.Close()
	var stacks map[string]*run.StackInstance
	if err := json.NewDecoder(rc).Decode(&stacks); err != nil {
		return fmt.Errorf("fsm: restore: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stacks = stacks
	return nil
}

func (f *FSM) State(stackID string) (*run.StackInstance, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	inst, ok := f.stacks[stackID]
	if !ok {
		return nil, false
	}
	cp := *inst
	return &cp, true
}

func (f *FSM) AllStates() map[string]run.StackInstance {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make(map[string]run.StackInstance, len(f.stacks))
	for k, v := range f.stacks {
		out[k] = *v
	}
	return out
}

type fsmSnapshot struct {
	stacks map[string]*run.StackInstance
}

func (s *fsmSnapshot) Persist(sink hraft.SnapshotSink) error {
	b, err := json.Marshal(s.stacks)
	if err != nil {
		sink.Cancel()
		return err
	}
	if _, err := sink.Write(b); err != nil {
		sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *fsmSnapshot) Release() {}
