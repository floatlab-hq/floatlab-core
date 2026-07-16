package agent

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/floatlab/floatlab-core/pkg/store"
)

const (
	FloatlabPool           = "floatlab"
	FloatlabSystemDataset  = "floatlab/system"
	FloatlabLogsDataset    = "floatlab/system/logs"
	FloatlabMetricsDataset = "floatlab/system/metrics"
)

var ErrFloatlabPoolMissing = errors.New("zfs dataset floatlab was not found")

type ZFS interface {
	DatasetExists(ctx context.Context, name string) (bool, error)
	CreateDataset(ctx context.Context, name string, req store.CreateDatasetRequest) error
	ListDatasets(ctx context.Context) ([]store.Dataset, error)
	GetDataset(ctx context.Context, name string) (store.Dataset, error)
	DeleteDataset(ctx context.Context, name string, recursive bool) error
	ListSnapshots(ctx context.Context, dataset string, recursive bool) ([]store.Snapshot, error)
	CreateSnapshot(ctx context.Context, dataset, snapshot string, recursive bool) error
	DeleteSnapshot(ctx context.Context, dataset, snapshot string, recursive bool) error
	SendSnapshot(ctx context.Context, dataset, snapshot, from string, recursive bool, dst io.Writer) error
	ReceiveSnapshot(ctx context.Context, dataset string, forceRollback bool, src io.Reader) error
}

func BootstrapSystemDatasets(ctx context.Context, zfs ZFS) error {
	poolExists, err := zfs.DatasetExists(ctx, FloatlabPool)
	if err != nil {
		return fmt.Errorf("check zfs pool %s: %w", FloatlabPool, err)
	}
	if !poolExists {
		return ErrFloatlabPoolMissing
	}

	for _, dataset := range []string{FloatlabSystemDataset, FloatlabLogsDataset, FloatlabMetricsDataset} {
		exists, err := zfs.DatasetExists(ctx, dataset)
		if err != nil {
			return fmt.Errorf("check zfs dataset %s: %w", dataset, err)
		}
		if exists {
			continue
		}
		if err := zfs.CreateDataset(ctx, dataset, store.CreateDatasetRequest{}); err != nil {
			return fmt.Errorf("create zfs dataset %s: %w", dataset, err)
		}
	}

	return nil
}
