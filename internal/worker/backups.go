package worker

import (
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
	"go.uber.org/zap"
	"time"
)

func (w *Worker) saveRecoveryPoint(ctx context.Context, p ReplTriggerPayload, result store.SendResult) error {
	if result.GUID == "" || !store.IsAutomatedSnapshot(result.Snapshot) || result.SnapshotCreatedUnix <= 0 || p.DestNodeID == "" || p.Generation == 0 {
		return nil
	}
	return w.db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO monitoring_recovery_points(stack_id,dataset,source_id,destination_id,generation,snapshot,guid,snapshot_time)
		      SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM backup_targets WHERE stack_id=? AND dataset=? AND source_node_id=? AND node_id=? AND generation=? AND enabled=1)
		      ON CONFLICT(stack_id,dataset,source_id,destination_id,generation) DO UPDATE
		      SET snapshot=excluded.snapshot,guid=excluded.guid,snapshot_time=excluded.snapshot_time
		      WHERE excluded.snapshot_time>monitoring_recovery_points.snapshot_time`,
		Params: []any{p.StackID, p.Dataset, p.NodeID, p.DestNodeID, p.Generation, result.Snapshot, result.GUID, result.SnapshotCreatedUnix, p.StackID, p.Dataset, p.NodeID, p.DestNodeID, p.Generation},
	}})
}

func (w *Worker) backupActive(ctx context.Context, p ReplTriggerPayload) (bool, error) {
	targets, err := w.store.BackupTargets(ctx, p.StackID)
	if err != nil {
		return false, err
	}
	for _, t := range targets {
		if t.Dataset == p.Dataset && t.NodeID == p.DestNodeID {
			return t.Enabled && (p.Generation == 0 || p.Generation == t.Generation) && (p.TargetRevision == "" || p.TargetRevision == t.Revision), nil
		}
	}
	return false, nil
}

// Disabled targets remain durable until their source acknowledges release and pruning.
func (w *Worker) cleanupBackups(ctx context.Context) error {
	targets, err := w.store.BackupTargets(ctx, "")
	if err != nil {
		return err
	}
	var first error
	for _, t := range targets {
		if !t.CleanupPending || t.Enabled {
			continue
		}
		if err := w.cleanupBackupTarget(ctx, t); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (w *Worker) replicationOutcome(ctx context.Context, p ReplTriggerPayload, cause error) error {
	return w.backupOutcome(ctx, p, "replication", cause)
}
func (w *Worker) backupOutcome(ctx context.Context, p ReplTriggerPayload, kind string, cause error) error {
	now := time.Now().UTC()
	status, action, message := "healthy", "resolve", "Recovered"
	if cause != nil {
		status, action, message = "failing", "open", cause.Error()
	}
	identity, _ := json.Marshal([]string{kind, p.NodeID, p.StackID, p.Dataset, p.DestNodeID, p.DestDataset})
	return monitoring.Record(ctx, w.db, nil, monitoring.Transition{
		Result: monitoring.Result{CheckID: "worker:" + string(identity), Producer: "worker", Kind: kind, Resource: monitoring.Resource{HostID: p.NodeID, WorkloadID: p.StackID, Dataset: p.Dataset, SourceID: p.NodeID, DestinationID: p.DestNodeID}, Observer: p.NodeID, Target: p.DestHost, Status: status, Reason: message, ObservedAt: now, EvaluatedAt: now},
		RuleID: kind, Title: kind, Message: message, Severity: "warning", Action: action,
	})
}

func (w *Worker) runBackupCleanup(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.cleanupBackups(ctx); err != nil {
				w.log.Warn("backup cleanup", zap.Error(err))
			}
		}
	}
}

func (w *Worker) cleanupBackupTarget(ctx context.Context, t config.BackupTarget) error {
	_, cleanupErr := w.pool.Execute(ctx, t.SourceNodeID, "fs.repl.cleanup", map[string]interface{}{"dataset": t.Dataset, "target_node_id": t.NodeID, "local_policy": t.LocalPolicy, "generation": t.Generation})
	message := ""
	pending := 0
	if cleanupErr != nil {
		message = cleanupErr.Error()
		pending = 1
	}
	if err := w.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE backup_targets SET cleanup_pending=?,error=? WHERE stack_id=? AND dataset=? AND node_id=? AND enabled=0 AND generation=?`, Params: []interface{}{pending, message, t.StackID, t.Dataset, t.NodeID, t.Generation}}}); err != nil {
		return err
	}
	return cleanupErr
}
