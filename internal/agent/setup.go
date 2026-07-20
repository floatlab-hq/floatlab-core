package agent

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var ErrSetupChecksFailed = errors.New("setup checks failed")

type SetupValidator interface {
	RunSetupChecks(ctx context.Context) SetupReport
}

type SetupReport struct {
	OK     bool         `json:"ok"`
	Checks []SetupCheck `json:"checks"`
}

type SetupCheck struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type CommandSetupValidator struct {
	ZFS ZFS
}

func (v CommandSetupValidator) RunSetupChecks(ctx context.Context) SetupReport {
	checks := []SetupCheck{
		checkCommand(ctx, "docker", "docker", "version"),
		checkCommand(ctx, "zfs", "zfs", "version"),
		checkCommand(ctx, "ip", "ip", "-Version"),
		checkCommand(ctx, "floatlab-lan", "ip", "link", "show", "dev", "floatlab-lan"),
		v.checkFloatlabDataset(ctx),
	}

	report := SetupReport{OK: true, Checks: checks}
	for _, check := range checks {
		if !check.OK {
			report.OK = false
			break
		}
	}
	return report
}

func (v CommandSetupValidator) checkFloatlabDataset(ctx context.Context) SetupCheck {
	exists, err := v.ZFS.DatasetExists(ctx, FloatlabPool)
	if err != nil {
		return SetupCheck{
			Name:    "floatlab-zfs-dataset",
			OK:      false,
			Message: fmt.Sprintf("failed to query %s: %v", FloatlabPool, err),
		}
	}
	if !exists {
		return SetupCheck{
			Name:    "floatlab-zfs-dataset",
			OK:      false,
			Message: "zfs dataset floatlab does not exist",
		}
	}
	return SetupCheck{
		Name:    "floatlab-zfs-dataset",
		OK:      true,
		Message: "zfs dataset floatlab exists",
	}
}

func checkCommand(ctx context.Context, name string, command string, args ...string) SetupCheck {
	if _, err := exec.LookPath(command); err != nil {
		return SetupCheck{
			Name:    name,
			OK:      false,
			Message: fmt.Sprintf("%s was not found in PATH", command),
		}
	}

	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return SetupCheck{Name: name, OK: false, Message: message}
	}

	message := strings.TrimSpace(string(out))
	if message == "" {
		message = "available"
	}
	return SetupCheck{Name: name, OK: true, Message: message}
}

func setupReportError(report SetupReport) error {
	if report.OK {
		return nil
	}

	failures := make([]string, 0)
	for _, check := range report.Checks {
		if !check.OK {
			failures = append(failures, fmt.Sprintf("%s: %s", check.Name, check.Message))
		}
	}
	return fmt.Errorf("%w: %s", ErrSetupChecksFailed, strings.Join(failures, "; "))
}
