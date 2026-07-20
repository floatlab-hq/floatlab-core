package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	raftpkg "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
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
	ops        *operation.Store
	db         *rqlite.Client
}

func New(
	store *config.Store,
	raft *raftpkg.Node,
	pool *hostclient.Pool,
	db *rqlite.Client,
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
		ops:        operation.NewStore(db),
		db:         db,
	}
}

// Run starts the orchestrator event loop. It blocks until ctx is cancelled.
func (o *Orchestrator) Run(ctx context.Context) error {
	o.resume(ctx)
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

func (o *Orchestrator) resume(ctx context.Context) {
	operations, err := o.ops.Active(ctx)
	if err != nil {
		o.log.Error("orchestrator: load active operations", zap.Error(err))
		return
	}
	for _, op := range operations {
		instance, ok := o.raft.FSM().State(op.StackID)
		if !ok {
			continue
		}
		switch instance.State {
		case run.StateProvisioning:
			go o.doProvision(ctx, op.StackID)
		case run.StateStarting:
			go o.doStart(ctx, op.StackID)
		case run.StateStopping:
			go o.doStop(ctx, op.StackID)
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

	spec, lifecycleErr := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
	var datasets []ipc.DatasetCreatePayload
	if lifecycleErr == nil {
		datasets = append(datasets, ipc.DatasetCreatePayload{Dataset: stack.ZFSDataset, BlockSize: "32K", Compression: "lz4"})
		for _, mount := range spec.Mounts {
			datasets = append(datasets, ipc.DatasetCreatePayload{Dataset: mount.Dataset, BlockSize: mount.RecordSize, Compression: mount.Compression, Quota: mount.Quota})
		}
	} else {
		parsed, err := compose.Parse(ctx, stack.ComposeYAML, stack.Name)
		if err != nil {
			o.log.Error("orchestrator: provision: parse compose", zap.String("id", stackID), zap.Error(err))
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
			_ = o.ops.FinishForStack(context.Background(), stackID, "create", "failed", err.Error())
			return
		}
		datasets = append(datasets, buildDatasetCreate(stack.Name, parsed.Extension))
	}
	if lifecycleErr == nil {
		if err := stageBindData(stack.Name, spec.Mounts); err != nil {
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
			_ = o.ops.FinishForStack(context.Background(), stackID, "start", "failed", err.Error())
			return
		}
	}
	for _, payload := range datasets {
		if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "fs.dataset.create", payload); err != nil {
			o.log.Error("orchestrator: provision: create dataset on primary", zap.String("id", stackID), zap.Error(err))
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
			_ = o.ops.FinishForStack(context.Background(), stackID, "create", "failed", err.Error())
			return
		}
	}
	if lifecycleErr == nil {
		if err := restoreBindData(stack.Name, spec.Mounts); err != nil {
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
			_ = o.ops.FinishForStack(context.Background(), stackID, "start", "failed", err.Error())
			return
		}
	}
	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "compose.source.write", ipc.ComposeSourcePayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: stack.ComposeYAML}); err != nil {
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
		_ = o.ops.FinishForStack(context.Background(), stackID, "start", "failed", err.Error())
		return
	}
	if lifecycleErr == nil && spec.HasPorts {
		if err := o.provisionNetwork(ctx, stack, spec); err != nil {
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventFailStack)
			_ = o.ops.FinishForStack(context.Background(), stackID, "create", "failed", err.Error())
			return
		}
	}
	if lifecycleErr == nil {
		_ = o.syncAlerts(ctx, stack.ID, spec.Alerts)
	}

	if stack.BackupNodeID != "" {
		for _, payload := range datasets {
			if _, err := o.pool.Execute(ctx, stack.BackupNodeID, "fs.dataset.create", payload); err != nil {
				o.log.Warn("orchestrator: provision: create dataset on secondary (non-fatal)", zap.String("id", stackID), zap.Error(err))
			}
		}
	}

	o.log.Info("orchestrator: provisioned", zap.String("stack", stack.Name))
	o.applyEvent(stack.PrimaryNodeID, stackID, run.EventProvisionDone)
	actor, operationID := o.activeOperationIdentity(ctx, stackID)
	_ = operation.RecordEvent(context.Background(), o.db, operation.Event{StackID: stackID, Type: "Created", Outcome: "succeeded", Actor: actor, OperationID: operationID})
	if lifecycleErr == nil {
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartStack)
	} else {
		_ = o.ops.FinishForStack(context.Background(), stackID, "create", "succeeded", "")
	}
}

func stageBindData(stackName string, mounts map[string]compose.Mount) error {
	root := filepath.Join("/floatlab", stackName)
	for _, mount := range mounts {
		if mount.Type != "bind" {
			continue
		}
		source := filepath.Join(root, filepath.Clean(mount.Source))
		staged := filepath.Join(root, ".floatlab-import-"+filepath.Base(mount.Dataset))
		if _, err := os.Stat(staged); err == nil {
			continue
		}
		if _, err := os.Stat(source); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := os.Rename(source, staged); err != nil {
			return fmt.Errorf("stage bind data %s: %w", mount.Source, err)
		}
	}
	return nil
}

func restoreBindData(stackName string, mounts map[string]compose.Mount) error {
	root := filepath.Join("/floatlab", stackName)
	for _, mount := range mounts {
		if mount.Type != "bind" {
			continue
		}
		target := filepath.Join(root, filepath.Base(mount.Dataset))
		staged := filepath.Join(root, ".floatlab-import-"+filepath.Base(mount.Dataset))
		if _, err := os.Stat(staged); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err := compose.CopyProject(staged, target); err != nil {
			return fmt.Errorf("import bind data %s: %w", mount.Source, err)
		}
		if err := os.RemoveAll(staged); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) provisionNetwork(ctx context.Context, stack *config.Stack, spec *compose.LifecycleSpec) error {
	pools, err := ipam.ListPools(ctx, o.db)
	if err != nil {
		return err
	}
	var selected *ipam.Pool
	for i := range pools {
		if pools[i].Name == spec.NetworkPool || (spec.NetworkPool == "" && pools[i].Default) {
			selected = &pools[i]
			break
		}
	}
	if selected == nil {
		return fmt.Errorf("orchestrator: no matching network pool")
	}
	address, err := ipam.AllocateIPv4(ctx, o.db, *selected, stack.ID)
	if err != nil {
		return err
	}
	prefix, _ := netip.ParsePrefix(selected.CIDR)
	host, peer := lifecycleVeth("flh", stack.ID), lifecycleVeth("flp", stack.ID)
	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "net.veth.ensure", ipc.VethPayload{StackID: stack.ID, HostName: host, PeerName: peer, Address: address + "/" + strconv.Itoa(prefix.Bits()), Bridge: "floatlab-lan"}); err != nil {
		_ = ipam.ReleaseIPv4(ctx, o.db, stack.ID)
		return err
	}
	if err := ipam.ActivateIPv4(ctx, o.db, stack.ID); err != nil {
		_, _ = o.pool.Execute(ctx, stack.PrimaryNodeID, "net.veth.delete", ipc.VethPayload{StackID: stack.ID, HostName: host})
		_ = ipam.ReleaseIPv4(ctx, o.db, stack.ID)
		return err
	}
	return o.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO stack_runtime(stack_id,stack_ip,network_pool,active_node_id,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(stack_id) DO UPDATE SET stack_ip=excluded.stack_ip,network_pool=excluded.network_pool,active_node_id=excluded.active_node_id,updated_at=excluded.updated_at`, Params: []interface{}{stack.ID, address, selected.ID, stack.PrimaryNodeID, time.Now().UTC()}}})
}

func (o *Orchestrator) syncAlerts(ctx context.Context, stackID string, alerts []compose.LifecycleAlert) error {
	statements := []rqlite.Statement{{SQL: `UPDATE stack_alert_rules SET active=0 WHERE stack_id=?`, Params: []interface{}{stackID}}}
	for _, alert := range alerts {
		selector := alert.Service
		if alert.Mount != "" {
			selector = alert.Mount
		}
		statements = append(statements, rqlite.Statement{SQL: `INSERT INTO stack_alert_rules(id,stack_id,name,metric,selector,comparator,threshold,duration,severity,message,active) VALUES(?,?,?,?,?,?,?,?,?,?,1) ON CONFLICT(stack_id,name) DO UPDATE SET metric=excluded.metric,selector=excluded.selector,comparator=excluded.comparator,threshold=excluded.threshold,duration=excluded.duration,severity=excluded.severity,message=excluded.message,active=1`, Params: []interface{}{uuid.NewString(), stackID, alert.Name, alert.Metric, selector, alert.Comparator, alert.Threshold, alert.Duration, alert.Severity, alert.Message}})
	}
	return o.db.Execute(ctx, statements)
}

func lifecycleVeth(prefix, stackID string) string {
	stackID = strings.ReplaceAll(stackID, "-", "")
	if len(stackID) > 10 {
		stackID = stackID[:10]
	}
	return prefix + stackID
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

	composeFile := stack.ComposeYAML
	if spec, err := compose.ParseLifecycle(stack.ComposeYAML, stack.Name); err == nil {
		stackIP := ""
		if spec.HasPorts {
			result, queryErr := o.db.Query(ctx, rqlite.Statement{SQL: `SELECT stack_ip FROM stack_runtime WHERE stack_id=?`, Params: []interface{}{stackID}})
			if queryErr != nil || len(result.Values) == 0 {
				o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartFailed)
				return
			}
			stackIP, _ = result.Values[0][0].(string)
		}
		if runtime, runtimeErr := compose.RuntimeYAML(stack.ComposeYAML, stack.Name, stackIP); runtimeErr == nil {
			composeFile = runtime
		} else {
			o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartFailed)
			return
		}
	}
	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "compose.pull", ipc.ComposePullPayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: composeFile}); err != nil {
		o.log.Error("orchestrator: start: compose.pull", zap.String("id", stackID), zap.Error(err))
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartFailed)
		_ = o.ops.FinishForStack(context.Background(), stackID, "start", "failed", err.Error())
		return
	}
	if _, err := o.pool.Execute(ctx, stack.PrimaryNodeID, "compose.up", ipc.ComposeUpPayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: composeFile}); err != nil {
		o.log.Error("orchestrator: start: compose.up", zap.String("id", stackID), zap.Error(err))
		o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartFailed)
		_ = o.ops.FinishForStack(context.Background(), stackID, "start", "failed", err.Error())
		return
	}

	o.log.Info("orchestrator: started", zap.String("stack", stack.Name))
	o.applyEvent(stack.PrimaryNodeID, stackID, run.EventStartDone)
	actor, operationID := o.activeOperationIdentity(ctx, stackID)
	_ = o.ops.FinishForStack(context.Background(), stackID, "start", "succeeded", "")
	_ = operation.RecordEvent(context.Background(), o.db, operation.Event{StackID: stackID, Type: "Start", Outcome: "succeeded", Actor: actor, OperationID: operationID})
}

func (o *Orchestrator) activeOperationIdentity(ctx context.Context, stackID string) (string, string) {
	op, err := o.ops.ActiveForStack(ctx, stackID)
	if err != nil || op == nil {
		return "system", ""
	}
	return op.Actor, op.ID
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
		_ = o.ops.FinishForStack(context.Background(), stackID, "stop", "failed", err.Error())
		return
	}

	o.log.Info("orchestrator: stopped", zap.String("stack", stack.Name))
	o.applyEvent(nodeID, stackID, run.EventStopDone)
	actor, operationID := o.activeOperationIdentity(ctx, stackID)
	_ = o.ops.FinishForStack(context.Background(), stackID, "stop", "succeeded", "")
	_ = operation.RecordEvent(context.Background(), o.db, operation.Event{StackID: stackID, Type: "Stop", Outcome: "succeeded", Actor: actor, OperationID: operationID})
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
