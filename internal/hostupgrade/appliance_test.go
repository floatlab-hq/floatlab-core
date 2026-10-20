//go:build appliance

package hostupgrade

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/floatlab/floatlab-core/internal/managementupdate"
	"github.com/floatlab/floatlab-core/internal/update"
)

func applianceManager(t *testing.T) *Manager {
	t.Helper()
	if os.Getenv("FLOATLAB_APPLIANCE_TEST") != "1" {
		t.Skip("real installed appliance only; opt in explicitly")
	}
	environment, err := (Linux{}).output(context.Background(), "systemctl", "show", "-p", "Environment", "--value", "floatlab-hostd.service")
	if err != nil {
		t.Fatalf("read production hostd execution environment: %v", err)
	}
	servicePath := ""
	for _, field := range strings.Fields(environment) {
		field = strings.Trim(field, `"`)
		if strings.HasPrefix(field, "PATH=") {
			if servicePath != "" {
				t.Fatal("ambiguous production hostd PATH")
			}
			servicePath = strings.TrimPrefix(field, "PATH=")
		}
	}
	if servicePath == "" {
		t.Fatal("production hostd PATH is missing")
	}
	for _, entry := range strings.Split(servicePath, ":") {
		if !strings.HasPrefix(entry, "/nix/store/") || filepath.Clean(entry) != entry {
			t.Fatalf("unexpected production hostd PATH entry %q", entry)
		}
	}
	if err = os.Setenv("PATH", servicePath); err != nil {
		t.Fatal(err)
	}
	if root, err := (Linux{}).output(context.Background(), "findmnt", "-n", "-o", "SOURCE", "/"); err != nil || root != "floatlab/os/live" {
		t.Fatalf("installed ZFS root required: %q %v", root, err)
	}
	m := NewDefault()
	management := managementupdate.NewDefault()
	m.Checkpoint = management.Checkpoint
	m.RestoreCheckpoint = management.RestoreCheckpoint
	m.ReleaseCheckpoint = management.ReleaseCheckpoint
	m.PreflightCheckpoint = management.PreflightCheckpoint
	m.TrialStart = management.StartCheckpointTrial
	m.TrialReady = management.ActivateCheckpointControllers
	m.TrialStop = management.StopCheckpoint
	m.ValidateManagement = management.ValidateCheckpointHost
	m.CleanupCheckpoint = management.CleanupCheckpoint
	return m
}
func applianceOperation(t *testing.T) Request {
	t.Helper()
	id := os.Getenv("FLOATLAB_HOST_TEST_OPERATION")
	if !operationID.MatchString(id) {
		t.Fatal("FLOATLAB_HOST_TEST_OPERATION must name this disposable fixture")
	}
	return Request{OperationID: id, Actor: "appliance-qualification", IdempotencyKey: id}
}
func applianceContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

type fixtureTransport struct {
	directory, tag string
	manifest       []byte
}

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	prefix := "/" + Repository + "/releases/download/" + f.tag + "/"
	if r.URL.Scheme != "https" || r.URL.Host != "github.com" || !strings.HasPrefix(r.URL.Path, prefix) {
		return nil, fmt.Errorf("test fixture refuses non-release request %s", r.URL)
	}
	var body io.ReadCloser
	switch strings.TrimPrefix(r.URL.Path, prefix) {
	case "host-manifest.json":
		body = io.NopCloser(strings.NewReader(string(f.manifest)))
	case "floatlab-root.tar.gz":
		file, err := os.Open(filepath.Join(f.directory, "floatlab-root.tar.gz"))
		if err != nil {
			return nil, err
		}
		body = file
	default:
		return nil, fmt.Errorf("unexpected fixture artifact")
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Header: make(http.Header), Body: body, Request: r}, nil
}

// Only distribution is replaced: the archive, ZFS, Docker, sandbox, EFI, and journals are real.
// The in-memory qualification entry allows measuring this transition before publishing evidence.
func TestHostApplianceStage(t *testing.T) {
	m := applianceManager(t)
	ctx := applianceContext(t)
	request := applianceOperation(t)
	directory := os.Getenv("FLOATLAB_HOST_FIXTURE_DIR")
	if directory == "" {
		t.Fatal("FLOATLAB_HOST_FIXTURE_DIR required")
	}
	raw, err := os.ReadFile(filepath.Join(directory, "host-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var target, source Manifest
	if err = json.Unmarshal(raw, &target); err != nil {
		t.Fatal(err)
	}
	sourceRaw, err := os.ReadFile("/floatlab-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(sourceRaw, &source); err != nil {
		t.Fatal(err)
	}
	if target.Version == source.Version {
		t.Fatal("candidate must be a distinct built OS release")
	}
	target.QualifiedSources = []string{source.Version}
	raw, err = json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	request.Release = "host-v" + target.Version
	previousClient := client
	client = &http.Client{Transport: fixtureTransport{directory, request.Release, raw}, Timeout: 25 * time.Minute}
	t.Cleanup(func() { client = previousClient })
	status, err := m.Prepare(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(25 * time.Minute)
	defer deadline.Stop()
	for status.State == "staging" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("OS staging deadline")
		case <-time.After(time.Second):
		}
		status, err = m.Status()
		if err != nil {
			t.Fatal(err)
		}
	}
	if status.State != "staged" {
		t.Fatalf("staging failed operation=%s state=%s phase=%s error=%s rollback_error=%s", status.OperationID, status.State, status.Phase, status.Error, status.RollbackError)
	}
	t.Logf("real OS staged source=%s target=%s operation=%s (qualification fixture; not a published support claim)", source.Version, target.Version, status.OperationID)
}

func TestHostApplianceActivate(t *testing.T) {
	m := applianceManager(t)
	status, err := m.Activate(applianceContext(t), applianceOperation(t))
	if err != nil {
		t.Fatal(err)
	}
	if status.State != "armed" {
		t.Fatalf("activation did not arm boot: %+v", status)
	}
}

type brokenTrialEFI struct{ Linux }

func (b brokenTrialEFI) Arm(ctx context.Context, s Status) error {
	if err := b.Linux.Arm(ctx, s); err != nil {
		return err
	}
	raw, err := os.ReadFile("/floatlab/system/etc/install-operation.json")
	if err != nil {
		return err
	}
	var op installer.Operation
	if err = json.Unmarshal(raw, &op); err != nil {
		return err
	}
	for i, disk := range op.Disks {
		mount := fmt.Sprintf("/run/floatlab-updates/%s/broken-efi-%d", s.OperationID, i)
		if err = os.MkdirAll(mount, 0700); err != nil {
			return err
		}
		if err = b.Linux.run(ctx, "mount", disk.Path+"-part1", mount); err != nil {
			return err
		}
		writeErr := update.WriteFile(mount+"/EFI/FloatLab/TRIAL.EFI", []byte("qualification: invalid EFI candidate\n"), 0600)
		unmountErr := b.Linux.run(ctx, "umount", mount)
		if writeErr != nil {
			return writeErr
		}
		if unmountErr != nil {
			return unmountErr
		}
	}
	return nil
}
func TestHostApplianceActivateBadEFI(t *testing.T) {
	m := applianceManager(t)
	m.Backend = brokenTrialEFI{}
	s, err := m.Activate(applianceContext(t), applianceOperation(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "armed" {
		t.Fatalf("bad EFI trial did not arm: %+v", s)
	}
}

func TestHostApplianceAssertTrial(t *testing.T) {
	m := applianceManager(t)
	ctx := applianceContext(t)
	s, err := m.StatusFor(applianceOperation(t).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "ready" {
		t.Fatalf("trial not ready: %+v", s)
	}
	version, origin, token, _, err := m.Backend.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != s.Manifest.Version || origin != "floatlab/os/"+version+"@base" || token != s.OperationID {
		t.Fatalf("trial boot mismatch: %s %s %s", version, origin, token)
	}
	linux := Linux{}
	bootfs, err := linux.output(ctx, "zpool", "get", "-H", "-o", "value", "bootfs", "floatlab")
	if err != nil || bootfs != "floatlab/os/current" {
		t.Fatalf("one-shot boot changed permanent boot: %q %v", bootfs, err)
	}
	current, err := linux.property(ctx, "origin", "floatlab/os/current")
	if err != nil || current != "floatlab/os/"+s.Source.Version+"@base" {
		t.Fatalf("trial replaced current: %q %v", current, err)
	}
	containers, err := linux.output(ctx, "docker", "ps", "-aq", "--filter", "label=com.docker.compose.project=floatlab-system")
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(containers)
	if len(ids) != 4 {
		t.Fatalf("expected four management containers: %q", containers)
	}
	args := append([]string{"inspect", "--format", "{{.HostConfig.RestartPolicy.Name}}"}, ids...)
	policies, err := linux.output(ctx, "docker", args...)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(policies)
	if len(fields) != 4 {
		t.Fatalf("expected four management containers: %q", policies)
	}
	for _, policy := range fields {
		if policy != "no" {
			t.Fatalf("restart policy escaped supervision: %s", policy)
		}
	}
	if err = m.Backend.Health(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestHostApplianceCommit(t *testing.T) {
	m := applianceManager(t)
	s, err := m.Commit(applianceContext(t), applianceOperation(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "committed" || !s.CommitFinalized {
		t.Fatalf("commit incomplete: %+v", s)
	}
}

func TestHostApplianceAssertRecovered(t *testing.T) {
	m := applianceManager(t)
	ctx := applianceContext(t)
	s, err := m.StatusFor(applianceOperation(t).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "recovered" {
		t.Fatalf("fallback did not restore management: %+v", s)
	}
	version, origin, token, _, err := m.Backend.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != s.Source.Version || origin != "floatlab/os/"+version+"@base" || token != "" {
		t.Fatalf("fallback boot mismatch: %s %s %s", version, origin, token)
	}
	if err = m.Backend.Health(ctx); err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile("/floatlab/system/management/raft/host-qualification.marker")
	if err != nil || string(marker) != "after-preflight" {
		t.Fatalf("restored the wrong checkpoint: %q %v", marker, err)
	}
}

func TestHostApplianceRollback(t *testing.T) {
	m := applianceManager(t)
	s, err := m.Rollback(applianceContext(t), applianceOperation(t))
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "recovering" {
		t.Fatalf("rollback not scheduled: %+v", s)
	}
}

func TestHostApplianceCleanup(t *testing.T) {
	m := applianceManager(t)
	r := applianceOperation(t)
	if _, err := m.Cleanup(applianceContext(t), r); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"floatlab-root.tar.gz", "floatlab-root.tar.gz.partial"} {
		if _, err := os.Stat(filepath.Join(m.Dir, r.OperationID, name)); !os.IsNotExist(err) {
			t.Fatalf("terminal operation download remains: %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("FLOATLAB_HOST_FIXTURE_DIR"), "floatlab-root.tar.gz")); err != nil {
		t.Fatalf("cleanup removed unrelated fixture archive: %v", err)
	}
}

func TestHostApplianceAssertCommitted(t *testing.T) {
	m := applianceManager(t)
	ctx := applianceContext(t)
	s, err := m.StatusFor(applianceOperation(t).OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "committed" || !s.CommitFinalized {
		t.Fatalf("commit journal incomplete: %+v", s)
	}
	version, origin, token, _, err := m.Backend.Running(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != s.Manifest.Version || origin != "floatlab/os/"+version+"@base" || token != "" {
		t.Fatalf("permanent boot mismatch: %s %s %s", version, origin, token)
	}
	previous, err := (Linux{}).property(ctx, "origin", "floatlab/os/previous")
	if err != nil || previous != "floatlab/os/"+s.Source.Version+"@base" {
		t.Fatalf("previous OS unavailable: %q %v", previous, err)
	}
	if _, err = os.Stat(filepath.Join(Directory, "management-os-"+s.OperationID+".json")); err != nil {
		t.Fatalf("previous management rollback point missing: %v", err)
	}
	engine, err := (Linux{}).actualEngine(ctx)
	if err != nil || engine != s.Manifest.Engine {
		t.Fatalf("candidate engine metadata differs from actual runtime: %+v %+v %v", engine, s.Manifest.Engine, err)
	}
	if err = m.Backend.Health(ctx); err != nil {
		t.Fatal(err)
	}
}
