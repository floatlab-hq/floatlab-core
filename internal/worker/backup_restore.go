package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func (w *Worker) SetBackupRestorePreparer(prepare func(context.Context, ipc.BackupRestorePreparePayload) (ipc.BackupRestorePrepareResult, error)) {
	w.prepareBackupRestore = prepare
}

func (w *Worker) handleBackupRestore(ctx context.Context, raw json.RawMessage) error {
	var p BackupRestorePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	if w.operationDone(ctx, p.OperationID) {
		return nil
	}
	if p.Lifecycle.Timestamp.IsZero() {
		// Adopt the committed generation once for jobs queued by older versions.
		instance, ok := w.raft.FSM().State(p.StackID)
		if !ok || instance.State != run.StateRestoring {
			return nil
		}
		p.Lifecycle = run.StackStateChanged{StackID: p.StackID, To: instance.State, Timestamp: instance.UpdatedAt, NodeID: p.DestinationNodeID}
		if err := w.saveBackupRestore(ctx, p); err != nil {
			return err
		}
	}
	if p.Lifecycle.Event == run.EventBackupRestorePrepared {
		// Data activation is complete. The orchestrator may already have advanced
		// beyond Provisioning before the worker checkpoints this task as done.
		if current, ok := w.raft.FSM().State(p.StackID); ok && current.State != run.StateRestoring && !current.UpdatedAt.Before(p.Lifecycle.Timestamp) {
			return nil
		}
	}
	if err := w.applyTaskLifecycle(p.Lifecycle); err != nil {
		return err
	}
	if p.Lifecycle.Event == run.EventBackupRestorePrepared {
		return nil
	}

	stack, err := w.store.GetStack(ctx, p.StackID)
	if err != nil {
		return err
	}
	if err := w.store.SyncBackupTargets(ctx, stack); err != nil {
		return err
	}
	if p.Prepared == nil {
		p.Prepared = map[string]BackupRestoreMount{}
	}
	if err := w.ops.Update(ctx, p.OperationID, "running", "restoring-data", ""); err != nil {
		return err
	}
	if _, err := w.pool.Execute(ctx, p.DestinationNodeID, "fs.backup.restore.reserve", ipc.BackupRestoreReservePayload{OperationID: p.OperationID, TargetDataset: p.DatasetPath}); err != nil {
		return err
	}
	destination, err := w.store.GetNode(ctx, p.DestinationNodeID)
	if err != nil {
		return err
	}
	host := ""
	for _, address := range destination.Addresses {
		if address.Type == "LAN-6" {
			host = address.Address
			break
		}
	}
	if p.BackupNodeID != p.DestinationNodeID && host == "" {
		return fmt.Errorf("restore destination has no LAN-6 address")
	}
	for _, selected := range p.Snapshots {
		mountID := path.Base(selected.Dataset)
		prepared, found := p.Prepared[selected.Dataset]
		if found && prepared.Activated {
			continue
		}
		if !found {
			request := ipc.BackupRestorePreparePayload{SourceNodeID: p.SourceNodeID, BackupNodeID: p.BackupNodeID, Dataset: selected.Dataset, Snapshot: selected.Snapshot, OperationID: p.OperationID, MountID: mountID, DestNodeID: p.DestinationNodeID, DestHost: host, DestPort: 22}
			var result ipc.BackupRestorePrepareResult
			if w.prepareBackupRestore != nil {
				result, err = w.prepareBackupRestore(ctx, request)
			} else {
				var body json.RawMessage
				body, err = w.pool.Execute(ctx, p.BackupNodeID, "fs.backup.restore.prepare", request)
				if err == nil {
					err = json.Unmarshal(body, &result)
				}
			}
			if err != nil {
				return err
			}
			if result.Dataset == "" || result.GUID == "" {
				return fmt.Errorf("restore preparation returned no verified dataset")
			}
			prepared = BackupRestoreMount{Dataset: result.Dataset, Snapshot: result.Snapshot, GUID: result.GUID}
			p.Prepared[selected.Dataset] = prepared
			if err := w.saveBackupRestore(ctx, p); err != nil {
				return err
			}
		}
		target := p.DatasetPath + "/" + mountID
		if _, err := w.pool.Execute(ctx, p.DestinationNodeID, "fs.backup.restore.activate", ipc.BackupRestoreActivatePayload{Dataset: prepared.Dataset, SourceNodeID: p.BackupNodeID, OperationID: p.OperationID, MountID: mountID, TargetDataset: target, Mountpoint: "/" + target, ExpectedGUID: prepared.GUID}); err != nil {
			return err
		}
		prepared.Activated = true
		p.Prepared[selected.Dataset] = prepared
		if err := w.saveBackupRestore(ctx, p); err != nil {
			return err
		}
	}
	if err := w.ops.Update(ctx, p.OperationID, "running", "provisioning", ""); err != nil {
		return err
	}
	if err := operation.RecordEvent(ctx, w.db, operation.Event{StackID: p.StackID, Type: "BackupDataRestored", Outcome: "succeeded", OperationID: p.OperationID, Actor: p.Actor}); err != nil {
		return err
	}
	p.Lifecycle = run.StackStateChanged{StackID: p.StackID, From: run.StateRestoring, ExpectedUpdatedAt: p.Lifecycle.Timestamp, To: run.StateProvisioning, Event: run.EventBackupRestorePrepared, Timestamp: time.Now().UTC(), NodeID: p.DestinationNodeID}
	if err := w.saveBackupRestore(ctx, p); err != nil {
		return err
	}
	return w.applyTaskLifecycle(p.Lifecycle)
}

func (w *Worker) saveBackupRestore(ctx context.Context, p BackupRestorePayload) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return w.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE tasks SET payload=? WHERE id=?`, Params: []interface{}{string(raw), "backup-restore-" + p.OperationID}}})
}
