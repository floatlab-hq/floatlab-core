package run

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrInvalidTransition   = errors.New("run: invalid state transition")
	ErrTransitionInProgress = errors.New("run: transition already in progress")
)

type State string
type Event string

const (
	StateProvisioning   State = "Provisioning"
	StateIdle           State = "Idle"
	StateStarting       State = "Starting"
	StateRunningPrimary State = "RunningPrimary"
	StateFailingOver    State = "FailingOver"
	StateRunningBackup  State = "RunningBackup"
	StateRestoring      State = "Restoring"
	StateStopping       State = "Stopping"
	StateUpdating       State = "Updating"
	StateRollingBack    State = "RollingBack"
	StateFailed         State = "Failed"
)

// Transitional states hold LockedBy; all other commands rejected while locked.
var transitionalStates = map[State]bool{
	StateProvisioning: true,
	StateStarting:     true,
	StateFailingOver:  true,
	StateRestoring:    true,
	StateStopping:     true,
	StateUpdating:     true,
	StateRollingBack:  true,
}

const (
	EventCreateStack    Event = "CreateStack"
	EventProvisionDone  Event = "ProvisionDone"
	EventStartStack     Event = "StartStack"
	EventStartDone      Event = "StartDone"
	EventStartFailed    Event = "StartFailed"
	EventFailStack      Event = "FailStack"
	EventFailoverStart  Event = "FailoverStart"
	EventFailoverDone   Event = "FailoverDone"
	EventFailoverFailed Event = "FailoverFailed"
	EventRestoreStart   Event = "RestoreStart"
	EventRestoreDone    Event = "RestoreDone"
	EventRestoreFailed  Event = "RestoreFailed"
	EventStopStack      Event = "StopStack"
	EventStopDone       Event = "StopDone"
	EventUpdateStack    Event = "UpdateStack"
	EventUpdateDone     Event = "UpdateDone"
	EventUpdateFailed   Event = "UpdateFailed"
	EventRollbackDone   Event = "RollbackDone"
	EventRollbackFailed Event = "RollbackFailed"
	EventPurgeStack     Event = "PurgeStack"
)

type Transition struct {
	From  State
	Event Event
	To    State
}

var transitions = []Transition{
	{StateIdle, EventCreateStack, StateProvisioning},
	{StateProvisioning, EventProvisionDone, StateIdle},
	{StateProvisioning, EventFailStack, StateFailed},
	{StateIdle, EventStartStack, StateStarting},
	{StateFailed, EventStartStack, StateStarting},
	{StateStarting, EventStartDone, StateRunningPrimary},
	{StateStarting, EventStartFailed, StateFailed},
	{StateRunningPrimary, EventFailStack, StateFailed},
	{StateRunningPrimary, EventFailoverStart, StateFailingOver},
	{StateFailingOver, EventFailoverDone, StateRunningBackup},
	{StateFailingOver, EventFailoverFailed, StateFailed},
	{StateRunningBackup, EventRestoreStart, StateRestoring},
	{StateRestoring, EventRestoreDone, StateRunningPrimary},
	{StateRestoring, EventRestoreFailed, StateFailed},
	{StateRunningPrimary, EventStopStack, StateStopping},
	{StateRunningBackup, EventStopStack, StateStopping},
	{StateIdle, EventStopStack, StateIdle},
	{StateStopping, EventStopDone, StateIdle},
	{StateRunningPrimary, EventUpdateStack, StateUpdating},
	{StateUpdating, EventUpdateDone, StateRunningPrimary},
	{StateUpdating, EventUpdateFailed, StateRollingBack},
	{StateRollingBack, EventRollbackDone, StateRunningPrimary},
	{StateRollingBack, EventRollbackFailed, StateFailed},
}

// transitionMap is indexed as from→event→to for O(1) lookup.
var transitionMap map[State]map[Event]State

func init() {
	transitionMap = make(map[State]map[Event]State)
	for _, t := range transitions {
		if transitionMap[t.From] == nil {
			transitionMap[t.From] = make(map[Event]State)
		}
		transitionMap[t.From][t.Event] = t.To
	}
}

type StackInstance struct {
	ID            string
	State         State
	PreviousState State
	LockedBy      string
	UpdatedAt     time.Time
}

type StateMachine interface {
	Apply(ctx context.Context, instance *StackInstance, event Event) (*StackInstance, error)
	ValidTransitions(state State) []Event
}

type machine struct{}

func New() StateMachine { return &machine{} }

func (m *machine) Apply(ctx context.Context, inst *StackInstance, ev Event) (*StackInstance, error) {
	if transitionalStates[inst.State] && inst.LockedBy != "" {
		// Only the completion/failure event for this transition may proceed.
		toMap := transitionMap[inst.State]
		if _, ok := toMap[ev]; !ok {
			return nil, ErrTransitionInProgress
		}
	}

	toMap, ok := transitionMap[inst.State]
	if !ok {
		return nil, fmt.Errorf("%w: no transitions from %s", ErrInvalidTransition, inst.State)
	}
	next, ok := toMap[ev]
	if !ok {
		return nil, fmt.Errorf("%w: %s -[%s]-> ?", ErrInvalidTransition, inst.State, ev)
	}

	updated := *inst
	updated.PreviousState = inst.State
	updated.State = next
	updated.UpdatedAt = time.Now().UTC()

	if transitionalStates[next] {
		updated.LockedBy = "control"
	} else {
		updated.LockedBy = ""
	}

	return &updated, nil
}

func (m *machine) ValidTransitions(state State) []Event {
	toMap := transitionMap[state]
	result := make([]Event, 0, len(toMap))
	for ev := range toMap {
		result = append(result, ev)
	}
	return result
}
