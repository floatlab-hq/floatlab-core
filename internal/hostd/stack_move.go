package hostd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func (d *Dispatcher) stackMoveActivate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.StackMoveActivatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	datasets, err := stackMoveDatasets(p.Datasets)
	if err != nil {
		return nil, err
	}
	err = store.ActivateStackMove(ctx, p.OperationID, p.StackID, p.SourceNodeID, p.DatasetPath, p.Snapshot, datasets)
	return map[string]bool{"activated": err == nil}, err
}

func (d *Dispatcher) stackMoveArchive(ctx context.Context, raw json.RawMessage) (any, error) {
	var p ipc.StackMoveArchivePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	datasets, err := stackMoveDatasets(p.Datasets)
	if err != nil {
		return nil, err
	}
	err = store.ArchiveStackMove(ctx, p.OperationID, p.DatasetPath, p.Snapshot, datasets)
	return map[string]bool{"archived": err == nil}, err
}

func stackMoveDatasets(payloads []ipc.DatasetCreatePayload) ([]store.DatasetParams, error) {
	datasets := make([]store.DatasetParams, 0, len(payloads))
	for _, p := range payloads {
		if !validDatasetProperties(p) {
			return nil, fmt.Errorf("invalid ZFS properties for %s", p.Dataset)
		}
		datasets = append(datasets, store.DatasetParams{Dataset: p.Dataset, BlockSize: p.BlockSize, Compression: p.Compression, Quota: p.Quota})
	}
	return datasets, nil
}
