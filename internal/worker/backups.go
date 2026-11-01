package worker

import (
	"context"
	"fmt"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
	"time"
)

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
	id := fmt.Sprintf("%s:%s:%s:%s", kind, p.NodeID, p.Dataset, p.DestNodeID)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if cause == nil {
		return w.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE alerts SET state='resolved',resolved_at=?,updated_at=? WHERE id=? AND state='active'`, Params: []interface{}{now, now, id}}})
	}
	return w.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO alerts(id,rule_id,stack_id,node_id,severity,kind,state,message,created_at,updated_at) VALUES(?,?,?,?,'warning',?,'active',?,?,?) ON CONFLICT(id) DO UPDATE SET state='active',message=excluded.message,resolved_at=NULL,updated_at=excluded.updated_at`, Params: []interface{}{id, kind, p.StackID, p.NodeID, kind, cause.Error(), now, now}}})
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
