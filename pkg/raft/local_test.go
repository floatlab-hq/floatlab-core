package raft

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestLocalLifecycleDurableBeforeNotificationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "local.sqlite")
	db, err := rqlite.OpenLocal(path, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalNode(ctx, db, "node-b", zap.NewNop()); err == nil {
		t.Fatal("accepted foreign database identity")
	}
	events, unsubscribe := n.FSM().Subscribe()
	defer unsubscribe()
	entry := run.StackStateChanged{StackID: "web", NodeID: "node-a", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}
	if err = n.Apply(entry, time.Second); err != nil {
		t.Fatal(err)
	}
	select {
	case actual := <-events:
		if actual.StackID != entry.StackID {
			t.Fatal(actual)
		}
	default:
		t.Fatal("missing lifecycle event")
	}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT entry FROM workload_lifecycle WHERE stack_id='web'`})
	if err != nil || len(rows.Values) != 1 {
		t.Fatal("event before durable row", rows, err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = rqlite.OpenLocal(path, "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	recovered, err := NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	state, ok := recovered.FSM().State("web")
	if !ok || state.State != run.StateRunningPrimary {
		t.Fatalf("lost lifecycle: %+v", state)
	}
	if !recovered.IsLeader() || recovered.Leader() != "node-a" {
		t.Fatal("local lifecycle requires remote leader")
	}
}

func TestRejectedLocalTransitionDoesNotReplaceDurableState(t *testing.T) {
	ctx := context.Background()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	initial := time.Unix(100, 0).UTC()
	if err := n.Apply(run.StackStateChanged{StackID: "web", NodeID: "node-a", To: run.StateRunningBackup, Timestamp: initial}, time.Second); err != nil {
		t.Fatal(err)
	}
	stopped := initial.Add(time.Second)
	if err := n.Apply(run.StackStateChanged{StackID: "web", NodeID: "node-a", From: run.StateRunningBackup, ExpectedUpdatedAt: initial, To: run.StateStopping, Event: run.EventStopStack, Timestamp: stopped}, time.Second); err != nil {
		t.Fatal(err)
	}
	err = n.Apply(run.StackStateChanged{StackID: "web", NodeID: "node-a", From: run.StateStarting, ExpectedUpdatedAt: initial, To: run.StateRunningPrimary, Event: run.EventStartDone, Timestamp: stopped.Add(time.Second)}, time.Second)
	if !errors.Is(err, run.ErrInvalidTransition) {
		t.Fatalf("stale completion accepted: %v", err)
	}
	restarted, err := NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	state, _ := restarted.FSM().State("web")
	if state.State != run.StateStopping || state.PreviousState != run.StateRunningBackup || !state.UpdatedAt.Equal(stopped) {
		t.Fatalf("rejected transition replaced committed snapshot: %+v", state)
	}
}

func TestLocalLifecycleRejectsForeignOwnerAndFailedCommit(t *testing.T) {
	ctx := context.Background()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	n, err := NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := n.FSM().Subscribe()
	defer unsubscribe()
	entry := run.StackStateChanged{StackID: "foreign", NodeID: "node-b", To: run.StateRunningPrimary}
	if err = n.Apply(entry, time.Second); err == nil {
		t.Fatal("accepted remote owner")
	}
	if err = db.Execute(ctx, []rqlite.Statement{{SQL: `CREATE TRIGGER reject_transition BEFORE INSERT ON workload_lifecycle BEGIN SELECT RAISE(ABORT,'simulated disk failure'); END`}}); err != nil {
		t.Fatal(err)
	}
	entry.NodeID = "node-a"
	if err = n.Apply(entry, time.Second); err == nil {
		t.Fatal("expected commit failure")
	}
	select {
	case <-events:
		t.Fatal("uncommitted lifecycle emitted")
	default:
	}
	if _, ok := n.FSM().State(entry.StackID); ok {
		t.Fatal("uncommitted lifecycle published")
	}
}
