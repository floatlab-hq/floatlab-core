package run

import "time"

// StackStateChanged is the Raft log entry type for state transitions.
// All nodes apply this to their in-memory state map via the FSM.
type StackStateChanged struct {
	StackID   string    `json:"stack_id"`
	From      State     `json:"from"`
	To        State     `json:"to"`
	Event     Event     `json:"event"`
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id,omitempty"`
}
