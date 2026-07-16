package agent

import (
	"context"
	"errors"
	"testing"
)

type fakeSetupValidator struct {
	report SetupReport
}

func (f fakeSetupValidator) RunSetupChecks(context.Context) SetupReport {
	return f.report
}

func TestSetupReportErrorPassesWhenAllChecksPass(t *testing.T) {
	report := SetupReport{
		OK: true,
		Checks: []SetupCheck{
			{Name: "docker", OK: true, Message: "available"},
			{Name: "docker-compose", OK: true, Message: "available"},
			{Name: "zfs", OK: true, Message: "available"},
			{Name: "floatlab-zfs-dataset", OK: true, Message: "zfs dataset floatlab exists"},
		},
	}

	if err := setupReportError(report); err != nil {
		t.Fatalf("setupReportError() error = %v, want nil", err)
	}
}

func TestSetupReportErrorFailsWhenARequiredCheckFails(t *testing.T) {
	report := SetupReport{
		OK: false,
		Checks: []SetupCheck{
			{Name: "docker", OK: true, Message: "available"},
			{Name: "docker-compose", OK: false, Message: "docker compose is missing"},
			{Name: "zfs", OK: true, Message: "available"},
			{Name: "floatlab-zfs-dataset", OK: true, Message: "zfs dataset floatlab exists"},
		},
	}

	err := setupReportError(report)
	if !errors.Is(err, ErrSetupChecksFailed) {
		t.Fatalf("setupReportError() error = %v, want %v", err, ErrSetupChecksFailed)
	}
}

func TestCommandSetupValidatorChecksFloatlabDataset(t *testing.T) {
	zfs := &fakeZFS{exists: map[string]bool{FloatlabPool: true}, errs: map[string]error{}}
	validator := CommandSetupValidator{ZFS: zfs}

	check := validator.checkFloatlabDataset(context.Background())
	if !check.OK {
		t.Fatalf("checkFloatlabDataset() = %+v, want ok", check)
	}
}

func TestCommandSetupValidatorReportsMissingFloatlabDataset(t *testing.T) {
	zfs := &fakeZFS{exists: map[string]bool{}, errs: map[string]error{}}
	validator := CommandSetupValidator{ZFS: zfs}

	check := validator.checkFloatlabDataset(context.Background())
	if check.OK {
		t.Fatalf("checkFloatlabDataset() = %+v, want failure", check)
	}
}
