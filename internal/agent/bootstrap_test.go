package agent

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/store"
)

type fakeZFS struct {
	exists  map[string]bool
	created []string
	errs    map[string]error
}

func (f *fakeZFS) DatasetExists(_ context.Context, name string) (bool, error) {
	if err := f.errs["exists:"+name]; err != nil {
		return false, err
	}
	return f.exists[name], nil
}

func (f *fakeZFS) CreateDataset(_ context.Context, name string, _ store.CreateDatasetRequest) error {
	if err := f.errs["create:"+name]; err != nil {
		return err
	}
	f.exists[name] = true
	f.created = append(f.created, name)
	return nil
}

func (f *fakeZFS) ListDatasets(context.Context) ([]store.Dataset, error) {
	return nil, nil
}

func (f *fakeZFS) GetDataset(context.Context, string) (store.Dataset, error) {
	return store.Dataset{}, nil
}

func (f *fakeZFS) DeleteDataset(context.Context, string, bool) error {
	return nil
}

func (f *fakeZFS) ListSnapshots(context.Context, string, bool) ([]store.Snapshot, error) {
	return nil, nil
}

func (f *fakeZFS) CreateSnapshot(context.Context, string, string, bool) error {
	return nil
}

func (f *fakeZFS) DeleteSnapshot(context.Context, string, string, bool) error {
	return nil
}

func (f *fakeZFS) SendSnapshot(context.Context, string, string, string, bool, io.Writer) error {
	return nil
}

func (f *fakeZFS) ReceiveSnapshot(context.Context, string, bool, io.Reader) error {
	return nil
}

func TestBootstrapSystemDatasetsCreatesMissingSystemDatasets(t *testing.T) {
	zfs := &fakeZFS{exists: map[string]bool{FloatlabPool: true}, errs: map[string]error{}}

	if err := BootstrapSystemDatasets(context.Background(), zfs); err != nil {
		t.Fatalf("BootstrapSystemDatasets() error = %v", err)
	}

	want := []string{FloatlabSystemDataset, FloatlabLogsDataset, FloatlabMetricsDataset}
	if !reflect.DeepEqual(zfs.created, want) {
		t.Fatalf("created datasets = %v, want %v", zfs.created, want)
	}
}

func TestBootstrapSystemDatasetsSkipsExistingDatasets(t *testing.T) {
	zfs := &fakeZFS{
		exists: map[string]bool{
			FloatlabPool:           true,
			FloatlabSystemDataset:  true,
			FloatlabLogsDataset:    true,
			FloatlabMetricsDataset: true,
		},
		errs: map[string]error{},
	}

	if err := BootstrapSystemDatasets(context.Background(), zfs); err != nil {
		t.Fatalf("BootstrapSystemDatasets() error = %v", err)
	}
	if len(zfs.created) != 0 {
		t.Fatalf("created datasets = %v, want none", zfs.created)
	}
}

func TestBootstrapSystemDatasetsRequiresFloatlabPool(t *testing.T) {
	zfs := &fakeZFS{exists: map[string]bool{}, errs: map[string]error{}}

	err := BootstrapSystemDatasets(context.Background(), zfs)
	if !errors.Is(err, ErrFloatlabPoolMissing) {
		t.Fatalf("BootstrapSystemDatasets() error = %v, want %v", err, ErrFloatlabPoolMissing)
	}
}
