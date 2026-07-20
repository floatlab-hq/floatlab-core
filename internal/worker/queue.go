package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
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
	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	pollInterval  = 5 * time.Second
	maxAttempts   = 5
	backoffFactor = 60 // attempt N waits N*backoffFactor seconds
)

// Handler processes a single task payload, returning an error to trigger retry.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Worker polls the rqlite tasks table and dispatches tasks via IPC to hostd.
type Worker struct {
	db       *rqlite.Client
	store    *config.Store
	pool     *hostclient.Pool
	hostname string
	handlers map[string]Handler
	log      *zap.Logger
	raft     *raftpkg.Node
	ops      *operation.Store
}

func New(db *rqlite.Client, cfgStore *config.Store, pool *hostclient.Pool, raft *raftpkg.Node, log *zap.Logger) *Worker {
	h, _ := os.Hostname()
	w := &Worker{
		db:       db,
		store:    cfgStore,
		pool:     pool,
		hostname: h,
		handlers: make(map[string]Handler),
		log:      log,
		raft:     raft,
		ops:      operation.NewStore(db),
	}
	w.registerHandlers()
	return w
}

func (w *Worker) registerHandlers() {
	w.handlers[TaskSnapshotCreate] = w.handleSnapshotCreate
	w.handlers[TaskSnapshotDelete] = w.handleSnapshotDelete
	w.handlers[TaskReplTrigger] = w.handleReplTrigger
	w.handlers[TaskStackUpgrade] = w.handleStackUpgrade
	w.handlers[TaskStackRestart] = w.handleStackRestart
	w.handlers[TaskStackDelete] = w.handleStackDelete
	w.handlers[TaskStackRestore] = w.handleStackRestore
}

// Run polls for pending tasks on the configured interval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	if err := w.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE tasks SET state='pending', locked_by=NULL WHERE state='running'`}}); err != nil {
		return fmt.Errorf("worker: recover running tasks: %w", err)
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.poll(ctx); err != nil {
				w.log.Error("worker: poll error", zap.Error(err))
			}
		}
	}
}

func (w *Worker) handleStackUpgrade(ctx context.Context, raw json.RawMessage) error {
	var p StackUpgradePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("stack.upgrade: parse: %w", err)
	}
	op, err := w.ops.Get(ctx, p.OperationID)
	if err != nil {
		return fmt.Errorf("stack.upgrade: operation: %w", err)
	}
	if op.State == "succeeded" || op.State == "failed" {
		return nil
	}
	stack, err := w.store.GetStack(ctx, p.StackID)
	if err != nil {
		return err
	}
	newRuntime, err := w.runtimeCompose(ctx, p.StackID, stack.Name, p.NewCompose)
	if err != nil {
		return err
	}
	oldRuntime, err := w.runtimeCompose(ctx, p.StackID, stack.Name, p.OldCompose)
	if err != nil {
		return err
	}
	snapshot := "pre-upgrade-" + p.OperationID
	fail := func(cause error) error {
		_ = w.applyStackEvent(p.StackID, p.NodeID, run.EventUpdateFailed)
		_ = w.restoreDatasets(ctx, p.NodeID, p.DatasetPath, snapshot, p.OperationID)
		_ = w.store.UpdateStackCompose(ctx, p.StackID, p.OldCompose)
		_, _ = w.pool.Execute(ctx, p.NodeID, "compose.source.write", ipc.ComposeSourcePayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.OldCompose})
		_, rollbackErr := w.pool.Execute(ctx, p.NodeID, "compose.up", ipc.ComposeUpPayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: oldRuntime})
		if rollbackErr == nil {
			_ = w.applyStackEvent(p.StackID, p.NodeID, run.EventRollbackDone)
		} else {
			_ = w.applyStackEvent(p.StackID, p.NodeID, run.EventRollbackFailed)
		}
		_ = w.ops.Update(context.Background(), p.OperationID, "failed", "rolled-back", cause.Error())
		_ = operation.RecordEvent(context.Background(), w.db, operation.Event{StackID: p.StackID, Type: "Upgrade", Outcome: "failed-rolled-back", OperationID: p.OperationID, Actor: p.Actor, Error: cause.Error()})
		return nil
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "pulling-images", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.pull", ipc.ComposePullPayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.NewCompose, Services: p.Services}); err != nil {
		return fail(err)
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "snapshotting", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{Dataset: p.DatasetPath, Name: snapshot, Recursive: true}); err != nil {
		return fail(err)
	}
	now := time.Now().UTC()
	_ = w.db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT OR IGNORE INTO stack_snapshots(id,stack_id,operation_id,zfs_name,kind,created_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{uuid.NewString(), p.StackID, p.OperationID, snapshot, "pre-upgrade", now}},
		{SQL: `INSERT OR IGNORE INTO recovery_points(id,stack_id,snapshot_id,dataset_path,compose_yaml,created_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{uuid.NewString(), p.StackID, snapshot, p.DatasetPath, p.OldCompose, now}},
	})
	_ = w.ops.Update(ctx, p.OperationID, "running", "updating-source", "")
	if err := w.store.UpdateStackCompose(ctx, p.StackID, p.NewCompose); err != nil {
		return fail(err)
	}
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.source.write", ipc.ComposeSourcePayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.NewCompose}); err != nil {
		return fail(err)
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "reconciling", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.up", ipc.ComposeUpPayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: newRuntime}); err != nil {
		return fail(err)
	}
	if err := w.waitHealthy(ctx, p.NodeID, p.StackID, timeout(p.HealthTimeout)); err != nil {
		return fail(err)
	}
	if err := w.applyStackEvent(p.StackID, p.NodeID, run.EventUpdateDone); err != nil {
		return fail(err)
	}
	_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "Upgrade", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor})
	return w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
}

func (w *Worker) handleStackRestart(ctx context.Context, raw json.RawMessage) error {
	var p StackRestartPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if w.operationDone(ctx, p.OperationID) {
		return nil
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "stopping", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.down", ipc.ComposeDownPayload{StackID: p.StackID, DatasetPath: p.DatasetPath}); err != nil {
		return err
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "starting", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.up", ipc.ComposeUpPayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.ComposeFile}); err != nil {
		return err
	}
	if err := w.waitHealthy(ctx, p.NodeID, p.StackID, timeout(p.HealthTimeout)); err != nil {
		return err
	}
	_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "Restart", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor})
	return w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
}

func (w *Worker) handleStackDelete(ctx context.Context, raw json.RawMessage) error {
	var p StackDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if w.operationDone(ctx, p.OperationID) {
		return nil
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "compose-down", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.down", ipc.ComposeDownPayload{StackID: p.StackID, DatasetPath: p.DatasetPath}); err != nil {
		return err
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "network-release", "")
	_, _ = w.pool.Execute(ctx, p.NodeID, "net.veth.delete", ipc.VethPayload{StackID: p.StackID, HostName: vethName("flh", p.StackID)})
	if err := ipam.ReleaseIPv4(ctx, w.db, p.StackID); err != nil {
		return err
	}
	if p.Purge {
		_ = w.ops.Update(ctx, p.OperationID, "running", "purging", "")
		if _, err := w.pool.Execute(ctx, p.NodeID, "fs.dataset.destroy", ipc.DatasetDestroyPayload{Dataset: p.DatasetPath, Recursive: true}); err != nil {
			return err
		}
	}
	if err := w.store.DeleteStack(ctx, p.StackID); err != nil {
		return err
	}
	_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "Delete", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor})
	return w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
}

func (w *Worker) handleStackRestore(ctx context.Context, raw json.RawMessage) error {
	var p StackRestorePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if w.operationDone(ctx, p.OperationID) {
		return nil
	}
	currentCompose := ""
	if current, err := w.store.GetStack(ctx, p.StackID); err == nil {
		currentCompose = current.ComposeYAML
	}
	if p.WasRunning {
		_ = w.ops.Update(ctx, p.OperationID, "running", "stopping", "")
		if _, err := w.pool.Execute(ctx, p.NodeID, "compose.down", ipc.ComposeDownPayload{StackID: p.StackID, DatasetPath: p.DatasetPath}); err != nil {
			return err
		}
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "preserving-current", "")
	if _, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{Dataset: p.DatasetPath, Name: "pre-restore-" + p.OperationID, Recursive: true}); err != nil {
		return err
	}
	_ = w.ops.Update(ctx, p.OperationID, "running", "activating-snapshot", "")
	if err := w.restoreDatasets(ctx, p.NodeID, p.DatasetPath, p.Snapshot, p.OperationID); err != nil {
		return err
	}
	if err := w.store.UpdateStackCompose(ctx, p.StackID, p.SourceCompose); err != nil {
		return err
	}
	_ = w.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO recovery_points(id,stack_id,snapshot_id,dataset_path,compose_yaml,created_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{uuid.NewString(), p.StackID, "pre-restore-" + p.OperationID, p.DatasetPath + "-recovery-" + p.OperationID, currentCompose, time.Now().UTC()}}})
	if _, err := w.pool.Execute(ctx, p.NodeID, "compose.source.write", ipc.ComposeSourcePayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.SourceCompose}); err != nil {
		return err
	}
	if p.WasRunning {
		_ = w.ops.Update(ctx, p.OperationID, "running", "starting", "")
		if _, err := w.pool.Execute(ctx, p.NodeID, "compose.up", ipc.ComposeUpPayload{StackID: p.StackID, DatasetPath: p.DatasetPath, ComposeFile: p.ComposeFile}); err != nil {
			return err
		}
		if err := w.waitHealthy(ctx, p.NodeID, p.StackID, timeout(p.HealthTimeout)); err != nil {
			return err
		}
	}
	_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "Restore", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor})
	return w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
}

func (w *Worker) restoreDatasets(ctx context.Context, nodeID, root, snapshot, operationID string) error {
	temporary := root + "-restore-" + operationID
	recovery := root + "-recovery-" + operationID
	parent := root
	if slash := strings.LastIndex(root, "/"); slash > 0 {
		parent = root[:slash]
	}
	raw, err := w.pool.Execute(ctx, nodeID, "fs.dataset.list", ipc.DatasetListPayload{Parent: parent})
	if err != nil {
		return err
	}
	var result ipc.DatasetListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	var current []ipc.DatasetInfoResult
	hasRoot, hasTemporary, hasRecovery := false, false, false
	for _, dataset := range result.Datasets {
		switch {
		case dataset.Name == root || strings.HasPrefix(dataset.Name, root+"/"):
			hasRoot = true
			current = append(current, dataset)
		case dataset.Name == temporary || strings.HasPrefix(dataset.Name, temporary+"/"):
			hasTemporary = true
		case dataset.Name == recovery || strings.HasPrefix(dataset.Name, recovery+"/"):
			hasRecovery = true
		}
	}
	// The swap completed before the worker stopped. Promoting again is harmless,
	// but cloning and renaming again would create a second recovery tree.
	if hasRoot && hasRecovery {
		return w.promoteTree(ctx, nodeID, root, result.Datasets)
	}
	// The old tree was renamed but the temporary clone was not activated yet.
	if !hasRoot && hasRecovery && hasTemporary {
		if _, err := w.pool.Execute(ctx, nodeID, "fs.dataset.rename", ipc.DatasetRenamePayload{Source: temporary, Target: root}); err != nil {
			return err
		}
		return w.promoteTree(ctx, nodeID, root, result.Datasets)
	}
	if !hasRoot {
		return fmt.Errorf("restore: source dataset %s is missing", root)
	}
	for _, dataset := range current {
		suffix := strings.TrimPrefix(dataset.Name, root)
		if _, err := w.pool.Execute(ctx, nodeID, "fs.dataset.clone", ipc.DatasetClonePayload{Snapshot: dataset.Name + "@" + snapshot, Target: temporary + suffix}); err != nil {
			return err
		}
	}
	if _, err := w.pool.Execute(ctx, nodeID, "fs.dataset.rename", ipc.DatasetRenamePayload{Source: root, Target: recovery}); err != nil {
		return err
	}
	if _, err := w.pool.Execute(ctx, nodeID, "fs.dataset.rename", ipc.DatasetRenamePayload{Source: temporary, Target: root}); err != nil {
		return err
	}
	return w.promoteTree(ctx, nodeID, root, current)
}

func (w *Worker) promoteTree(ctx context.Context, nodeID, root string, datasets []ipc.DatasetInfoResult) error {
	for _, dataset := range datasets {
		name := dataset.Name
		temporaryPrefix := root + "-restore-"
		if name != root && strings.HasPrefix(name, temporaryPrefix) {
			if slash := strings.Index(name[len(temporaryPrefix):], "/"); slash >= 0 {
				name = root + name[len(temporaryPrefix)+slash:]
			} else {
				name = root
			}
		}
		if name != root && !strings.HasPrefix(name, root+"/") {
			continue
		}
		if _, err := w.pool.Execute(ctx, nodeID, "fs.dataset.promote", ipc.DatasetPromotePayload{Dataset: name}); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) waitHealthy(ctx context.Context, nodeID, stackID string, duration time.Duration) error {
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		raw, err := w.pool.Execute(ctx, nodeID, "docker.list", ipc.DockerListPayload{StackID: stackID})
		if err == nil {
			var result ipc.DockerListResult
			if json.Unmarshal(raw, &result) == nil && len(result.Containers) > 0 {
				ready := true
				for _, container := range result.Containers {
					if container.State != "running" || (container.Health != "healthy" && container.Health != "none") {
						ready = false
					}
					if container.Health == "unhealthy" {
						return fmt.Errorf("stack %s has an unhealthy container", stackID)
					}
				}
				if ready {
					return nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("stack %s did not become healthy within %s", stackID, duration)
		case <-ticker.C:
		}
	}
}

func timeout(value string) time.Duration {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 2 * time.Minute
	}
	return duration
}

func vethName(prefix, stackID string) string {
	stackID = strings.ReplaceAll(stackID, "-", "")
	if len(stackID) > 10 {
		stackID = stackID[:10]
	}
	return prefix + stackID
}

func (w *Worker) operationDone(ctx context.Context, id string) bool {
	op, err := w.ops.Get(ctx, id)
	return err == nil && (op.State == "succeeded" || op.State == "failed")
}

func (w *Worker) runtimeCompose(ctx context.Context, stackID, name, source string) (string, error) {
	stackIP := ""
	result, err := w.db.Query(ctx, rqlite.Statement{SQL: `SELECT stack_ip FROM stack_runtime WHERE stack_id=?`, Params: []interface{}{stackID}})
	if err == nil && len(result.Values) > 0 {
		stackIP, _ = result.Values[0][0].(string)
	}
	return compose.RuntimeYAML(source, name, stackIP)
}

func (w *Worker) applyStackEvent(stackID, nodeID string, event run.Event) error {
	instance, ok := w.raft.FSM().State(stackID)
	if !ok {
		return fmt.Errorf("stack %s has no state", stackID)
	}
	next, err := run.New().Apply(context.Background(), instance, event)
	if err != nil {
		return err
	}
	return w.raft.Apply(run.StackStateChanged{StackID: stackID, From: instance.State, To: next.State, Event: event, Timestamp: time.Now().UTC(), NodeID: nodeID}, 10*time.Second)
}

func (w *Worker) poll(ctx context.Context) error {
	claimSQL := `
		UPDATE tasks
		SET state = 'running', locked_by = ?, updated_at = datetime('now')
		WHERE id = (
			SELECT id FROM tasks
			WHERE state = 'pending'
			  AND attempts < ?
			  AND (
			      attempts = 0
			      OR updated_at <= datetime('now', '-' || (attempts * ?) || ' seconds')
			  )
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING id, type, payload, attempts`

	result, err := w.db.Query(ctx, rqlite.Statement{
		SQL:    claimSQL,
		Params: []interface{}{w.hostname, maxAttempts, backoffFactor},
	})
	if err != nil {
		return fmt.Errorf("worker: claim query: %w", err)
	}
	if len(result.Values) == 0 {
		return nil
	}

	row := result.Values[0]
	taskID, _ := row[0].(string)
	taskType, _ := row[1].(string)
	payloadStr, _ := row[2].(string)
	attempts, _ := row[3].(float64)

	w.log.Info("worker: executing task",
		zap.String("id", taskID),
		zap.String("type", taskType),
		zap.Int("attempt", int(attempts)+1),
	)

	handler, ok := w.handlers[taskType]
	if !ok {
		w.failTask(ctx, taskID, fmt.Sprintf("no handler for task type %q", taskType))
		return nil
	}

	if execErr := handler(ctx, json.RawMessage(payloadStr)); execErr != nil {
		w.log.Error("worker: task failed",
			zap.String("id", taskID),
			zap.String("type", taskType),
			zap.Error(execErr),
		)
		if int(attempts)+1 >= maxAttempts {
			_ = w.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE tasks SET state='failed',attempts=attempts+1,error=?,updated_at=datetime('now'),locked_by=NULL WHERE id=?`, Params: []interface{}{execErr.Error(), taskID}}})
			var payload map[string]interface{}
			if json.Unmarshal([]byte(payloadStr), &payload) == nil {
				if operationID, ok := payload["operation_id"].(string); ok {
					_ = w.ops.Update(ctx, operationID, "failed", "failed", execErr.Error())
					stackID, _ := payload["stack_id"].(string)
					_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: stackID, Type: taskType, Outcome: "failed", OperationID: operationID, Error: execErr.Error()})
				}
			}
		} else {
			w.failTask(ctx, taskID, execErr.Error())
		}
		return nil
	}

	return w.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE tasks SET state = 'done', updated_at = datetime('now'), locked_by = NULL WHERE id = ?`,
		Params: []interface{}{taskID},
	}})
}

func (w *Worker) failTask(ctx context.Context, id, errMsg string) {
	_ = w.db.Execute(ctx, []rqlite.Statement{{
		SQL: `UPDATE tasks
		      SET state = 'pending', attempts = attempts + 1, error = ?, updated_at = datetime('now'), locked_by = NULL
		      WHERE id = ?`,
		Params: []interface{}{errMsg, id},
	}})
}

func (w *Worker) handleSnapshotCreate(ctx context.Context, raw json.RawMessage) error {
	var p SnapshotCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("snapshot.create: parse: %w", err)
	}
	var name string
	if p.Name != "" {
		name = p.Name
	} else if p.SnapType == "user" {
		name = store.UserSnapshotName(p.Label)
	} else {
		name = store.ScheduledSnapshotName(p.SnapType)
	}
	if _, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{
		Dataset:   p.Dataset,
		Name:      name,
		Recursive: p.Recursive,
	}); err != nil {
		return fmt.Errorf("snapshot.create %s: %w", p.Dataset, err)
	}
	if p.Keep > 0 {
		if err := w.applyRetentionViaIPC(ctx, p.NodeID, p.Dataset, p.SnapType, p.Keep); err != nil {
			w.log.Warn("worker: retention cleanup failed", zap.String("dataset", p.Dataset), zap.Error(err))
		}
	}
	if p.OperationID != "" {
		_ = operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "Snapshot", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor})
		return w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
	}
	return nil
}

func (w *Worker) handleSnapshotDelete(ctx context.Context, raw json.RawMessage) error {
	var p SnapshotDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("snapshot.delete: parse: %w", err)
	}
	_, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.destroy", ipc.SnapshotDestroyPayload{
		Dataset: p.Dataset,
		Name:    p.Snapshot,
	})
	if err == nil && p.RecoveryDataset != "" {
		_, err = w.pool.Execute(ctx, p.NodeID, "fs.dataset.destroy", ipc.DatasetDestroyPayload{Dataset: p.RecoveryDataset, Recursive: true})
	}
	if err == nil && p.OperationID != "" {
		_ = w.ops.Update(ctx, p.OperationID, "succeeded", "succeeded", "")
	}
	return err
}

func (w *Worker) handleReplTrigger(ctx context.Context, raw json.RawMessage) error {
	var p ReplTriggerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("repl.trigger: parse: %w", err)
	}
	jobID := fmt.Sprintf("repl-%s-%d", p.StackID, time.Now().UnixMilli())
	_, err := w.pool.Execute(ctx, p.NodeID, "fs.repl.send", ipc.ReplSendPayload{
		Dataset:  p.Dataset,
		DestHost: p.DestHost,
		DestPort: p.DestPort,
		JobID:    jobID,
	})
	return err
}

// applyRetentionViaIPC lists snapshots of the given type and destroys those
// beyond the keep limit, using IPC calls to the target node.
func (w *Worker) applyRetentionViaIPC(ctx context.Context, nodeID, dataset, snapType string, keep int) error {
	raw, err := w.pool.Execute(ctx, nodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: dataset})
	if err != nil {
		return fmt.Errorf("retention list: %w", err)
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("retention parse: %w", err)
	}

	prefix := store.PrefixScheduled + "-"
	suffix := "-" + snapType
	var matching []ipc.SnapshotInfoResult
	for _, s := range result.Snapshots {
		if strings.HasPrefix(s.Name, prefix) && strings.HasSuffix(s.Name, suffix) {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Name > matching[j].Name })

	for i := keep; i < len(matching); i++ {
		if _, err := w.pool.Execute(ctx, nodeID, "fs.snapshot.destroy", ipc.SnapshotDestroyPayload{
			Dataset: matching[i].Dataset,
			Name:    matching[i].Name,
		}); err != nil {
			return fmt.Errorf("retention destroy %s@%s: %w", matching[i].Dataset, matching[i].Name, err)
		}
	}
	return nil
}

// EnqueueTask inserts a new pending task into the rqlite tasks table.
func EnqueueTask(ctx context.Context, db *rqlite.Client, id, taskType, stackID string, payload interface{}) error {
	p := marshalPayload(payload)
	return db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO tasks (id, type, stack_id, payload, state, attempts, created_at, updated_at)
		      VALUES (?, ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		Params: []interface{}{id, taskType, stackID, p},
	}})
}
