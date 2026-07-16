package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/store"
)

func TestListDatasetsHandler(t *testing.T) {
	zfs := &listZFS{
		datasets: []store.Dataset{
			{Name: FloatlabPool, Mountpoint: "/floatlab", UsedBytes: 1, AvailableBytes: 10},
		},
	}
	svc := NewServiceWithDependencies(Config{}, slog.Default(), zfs, fakeSetupValidator{})

	req := httptest.NewRequest(http.MethodGet, "/zfs/datasets", nil)
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Datasets []store.Dataset `json:"datasets"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Datasets) != 1 || body.Datasets[0].Name != FloatlabPool {
		t.Fatalf("datasets = %+v, want floatlab dataset", body.Datasets)
	}
}

func TestSetupChecksHandlerReturnsServiceUnavailableWhenSetupFails(t *testing.T) {
	zfs := &listZFS{}
	setup := fakeSetupValidator{
		report: SetupReport{
			OK: false,
			Checks: []SetupCheck{
				{Name: "docker", OK: false, Message: "docker was not found in PATH"},
			},
		},
	}
	svc := NewServiceWithDependencies(Config{}, slog.Default(), zfs, setup)

	req := httptest.NewRequest(http.MethodGet, "/setup/checks", nil)
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

type listZFS struct {
	datasets []store.Dataset
	gotName  string
}

func (l *listZFS) DatasetExists(context.Context, string) (bool, error) {
	return true, nil
}

func (l *listZFS) CreateDataset(_ context.Context, name string, _ store.CreateDatasetRequest) error {
	l.gotName = name
	return nil
}

func (l *listZFS) ListDatasets(context.Context) ([]store.Dataset, error) {
	return l.datasets, nil
}

func (l *listZFS) GetDataset(_ context.Context, name string) (store.Dataset, error) {
	l.gotName = name
	return store.Dataset{Name: name}, nil
}

func (l *listZFS) DeleteDataset(_ context.Context, name string, _ bool) error {
	l.gotName = name
	return nil
}

func (l *listZFS) ListSnapshots(context.Context, string, bool) ([]store.Snapshot, error) {
	return nil, nil
}

func (l *listZFS) CreateSnapshot(context.Context, string, string, bool) error {
	return nil
}

func (l *listZFS) DeleteSnapshot(context.Context, string, string, bool) error {
	return nil
}

func (l *listZFS) SendSnapshot(context.Context, string, string, string, bool, io.Writer) error {
	return nil
}

func (l *listZFS) ReceiveSnapshot(context.Context, string, bool, io.Reader) error {
	return nil
}

func TestPathDatasetHandlerKeepsSlashesInDatasetName(t *testing.T) {
	zfs := &listZFS{}
	svc := NewServiceWithDependencies(Config{}, slog.Default(), zfs, fakeSetupValidator{})

	req := httptest.NewRequest(http.MethodGet, "/zfs/dataset/floatlab/app/db", nil)
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if zfs.gotName != "floatlab/app/db" {
		t.Fatalf("dataset = %q, want floatlab/app/db", zfs.gotName)
	}
}
