package hostd

import (
	"context"
	"encoding/json"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func (d *Dispatcher) backupRestorePrepare(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.BackupRestorePreparePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return store.PrepareBackupRestore(ctx, store.BackupRestorePrepareRequest{SourceNodeID: p.SourceNodeID, BackupNodeID: p.BackupNodeID, Dataset: p.Dataset, Snapshot: p.Snapshot, OperationID: p.OperationID, MountID: p.MountID, DestNodeID: p.DestNodeID, DestHost: p.DestHost, DestPort: p.DestPort})
}

func (d *Dispatcher) backupRestoreActivate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.BackupRestoreActivatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	err := store.ActivateBackupRestore(ctx, store.BackupRestoreActivateRequest{Dataset: p.Dataset, SourceNodeID: p.SourceNodeID, OperationID: p.OperationID, MountID: p.MountID, TargetDataset: p.TargetDataset, Mountpoint: p.Mountpoint, ExpectedGUID: p.ExpectedGUID})
	return map[string]bool{"activated": err == nil}, err
}

func (d *Dispatcher) backupRestoreReserve(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.BackupRestoreReservePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	err := store.ReserveBackupRestore(ctx, p.OperationID, p.TargetDataset)
	return map[string]bool{"reserved": err == nil}, err
}
