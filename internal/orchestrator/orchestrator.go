package orchestrator

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	raftpkg "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

// Orchestrator drives the stack state machine by reacting to:
//   - Raft FSM state change notifications → dispatch IPC commands for transitional states
//   - IPC events from hostd               → container health/state → FSM events
//   - Config store change events          → new node registrations → start event fanin
type Orchestrator struct {
	store *config.Store
	raft  *raftpkg.Node
	pool  *hostclient.Pool
	sm    run.StateMachine
	log   *zap.Logger

	mu         sync.Mutex
	nodeEvents map[string]struct{} // tracks which nodes we've subscribed to
	merged     chan ipc.Event      // fanin from all node IPC event channels
}

func New(
	store *config.Store,
	raft *raftpkg.Node,
	pool *hostclient.Pool,
	log *zap.Logger,
) *Orchestrator {
	return &Orchestrator{
		store:      store,
		raft:       raft,
		pool:       pool,
		sm:         run.New(),
		log:        log,
		nodeEvents: make(map[string]struct{}),
		merged:     make(chan ipc.Event, 256),
	}
}

// Run starts the orchestrator event loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	configChanges := o.store.Watch()
	stateChanges, unsub := o.raft.FSM().Subscribe()
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-configChanges:
			o.handleConfigChange(ctx, ev)
		case entry, ok := <-stateChanges:
			if !ok {
				return nil
			}
			o.handleStateChange(ctx, entry)
		case ev := <-o.merged:
			o.handleIPCEvent(ctx, ev)
		}
	}
}

// AddNode begins multiplexing IPC events from the given node into the orchestrator loop.
// Safe to call multiple times for the same node.
func (o *Orchestrator) AddNode(nodeID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, already := o.nodeEvents[nodeID]; already {
		return
	}
	ch := o.pool.Events(nodeID)
	if ch == nil {
		return
	}
	o.nodeEvents[nodeID] = struct{}{}
	go func() {
		for ev := range ch {
			select {
			case o.merged <- ev:
			default:
			}
		}
	}()
}

func (o *Orchestrator) handleConfigChange(ctx context.Context, ev config.ChangeEvent) {
	if ev.Entity == "node" && ev.Action == "create" {
		o.AddNode(ev.ID)
	}
}

func (o *Orchestrator) handleStateChange(ctx context.Context, entry run.StackStateChanged) {
	switch entry.To {
	case run.StateProvisioning:
		go o.doProvision(ctx, entry.StackID)
	case run.StateStarting:
		go o.doStart(ctx, entry.StackID)
	case run.StateStopping:
		go o.doStop(ctx, entry.StackID)
	// StateFailingOver, StateRestoring, StateUpdating → Sprint 3
	}
}

func (o *Orchestrator) handleIPCEvent(ctx context.Context, ev ipc.Event) {
	switch ev.Name {
	case "container.state":
		var e ipc.ContainerStateEvent
		if err := json.Unmarshal(ev.Payload, &e); err != nil {
			return
		}
		o.handleContainerState(ctx, e)
	case "container.health":
		var e ipc.ContainerHealthEvent
		if err := json.Unmarshal(ev.Payload, &e); err != nil {
			return
		}
		o.handleContainerHealth(ctx, e)
	case "hostd.ready":
		var e ipc.HostdReadyEvent
		if err := json.Unmarshal(ev.Payload, &e); err != nil {
			return
		}
		o.log.Info("hostd ready", zap.String("host", e.Hostname))
	}
}

// doProvision creates ZFS datasets for a stack entering Provisioning state.
func (o *Orchestrator) doProvision(ctx context.Context, stackID string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	stack, err := o.store.GetStack(ctx, stackID)
	if err != nil {
		o.log.Error("orchestrator: provision: get stack", zap.String("id", stackID), zap.Error(err))
		return
	}

	parsed, err := compose.Parse(ctx, stack.ComposeYAML, stack.Name)
	if err != nil {
		o.log.Error("orchestrator: provision: parse compose", zap.String("id", stackID), zap.Error(err))
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
		return
	}

	payload := buildDatasetCreate(stack.Name, parsed.Extension)
	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "fs.dataset.create", payload); err != nil {
		o.log.Error("orchestrator: provision: create dataset on primary", zap.String("id", stackID), zap.Error(err))
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
		return
	}

	if stack.BackupNodeID != "" {
		if _, err := o.pool.Execute(ctx, stack.BackupNodeID, "fs.dataset.create", payload); err != nil {
			o.log.Warn("orchestrator: provision: create dataset on secondary (non-fatal)",
				zap.String("id", stackID), zap.Error(err))
		}
	}

	o.log.Info("orchestrator: provisioned", zap.String("stack", stack.Name))
	o.applyEvent(stack.PrimaryNodeID, stackID, run.EventProvisionDone)
}

// doStart runs compose.up on the primary node for a stack entering Starting state.
func (o *Orchestrator) doStart(ctx context.Context, stackID string) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	stack, err := o.store.GetStack(ctx, stackID)
	if err != nil {
		o.log.Error("orchestrator: start: get stack", zap.String("id", stackID), zap.Error(err))
		return
	}

	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "compose.up", buildComposeUp(stack)); err != nil {
		o.log.Error("orchestrator: start: compose.up", zap.String("id", stackID), zap.Error(err))
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartFailed)
		return
	}

	o.log.Info("orchestrator: started", zap.String("stack", stack.Name))
	o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartDone)
}

// doStop runs compose.down on the active node for a stack entering Stopping state.
func (o *Orchestrator) doStop(ctx context.Context, stackID string) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	stack, err := o.store.GetStack(ctx, stackID)
	if err != nil {
		o.log.Error("orchestrator: stop: get stack", zap.String("id", stackID), zap.Error(err))
		return
	}

	nodeID := stack.PrimaryNodeID
	if inst, ok := o.raft.FSM().State(stackID); ok && inst.State == run.StateRunningBackup {
		nodeID = stack.BackupNodeID
	}

	if _, err := o.pool.Execute(ctx, nodeID, "compose.down", buildComposeDown(stack)); err != nil {
		o.log.Error("orchestrator: stop: compose.down", zap.String("id", stackID), zap.Error(err))
	}

	o.log.Info("orchestrator: stopped", zap.String("stack", stack.Name))
	o.applyEvent(nodeID, stackID, run.EventStopDone)
}

// applyEvent runs a state machine event against the FSM's current state and Raft-applies it.
func (o *Orchestrator) applyEvent(nodeID, stackID string, ev run.Event) {
	inst, ok := o.raft.FSM().State(stackID)
	if !ok {
		inst = &run.StackInstance{ID: stackID}
	}

	updated, err := o.sm.Apply(context.Background(), inst, ev)
	if err != nil {
		o.log.Error("orchestrator: FSM apply rejected",
			zap.String("stack", stackID),
			zap.String("state", string(inst.State)),
			zap.String("event", string(ev)),
			zap.Error(err))
		return
	}

	entry := run.StackStateChanged{
		StackID:   stackID,
		From:      inst.State,
		To:        updated.State,
		Event:     ev,
		Timestamp: time.Now().UTC(),
		NodeID:    nodeID,
	}
	if err := o.raft.Apply(entry, 10*time.Second); err != nil {
		o.log.Error("orchestrator: Raft apply failed",
			zap.String("stack", stackID),
			zap.Error(err))
	}
}
