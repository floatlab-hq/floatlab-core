//go:build appliance

package managementupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"gopkg.in/yaml.v3"
)

// Only artifact distribution is replaced: all storage, isolation, services,
// health checks, fencing, and recovery run against the disposable appliance.
type applianceFixture struct {
	*Linux
	failure   string
	candidate string
}

const applianceTargetVersion = "0.1.1-dev"
const applianceTargetRelease = "manage-v" + applianceTargetVersion

func TestApplianceSandboxFirewallParser(t *testing.T) {
	requireAppliance(t)
	path := filepath.Join(t.TempDir(), "isolation.nft")
	if err := os.WriteFile(path, []byte(sandboxFirewallRules("floatlab_acceptance_parser", "ftparser")), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("nft", "--check", "--file", path).CombinedOutput()
	if err != nil {
		t.Fatalf("real nft parser rejected isolation rules: %s %v", out, err)
	}
}

func TestApplianceManagementRecoverRetained(t *testing.T) {
	requireAppliance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	m := NewDefault()
	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := m.Status("")
	if err != nil {
		t.Fatal(err)
	}
	if s.State != "rolled_back" || !s.Authorized {
		t.Fatalf("retained checkpoint recovery %s", applianceSummary(s))
	}
}

func (b *applianceFixture) Preflight(ctx context.Context, s *Status) error {
	if err := b.Linux.Preflight(ctx, s); err != nil {
		return err
	}
	if b.failure == "preflight" {
		return errors.New("injected failed internal self-test after real sandbox validation")
	}
	return nil
}

func (b *applianceFixture) Prepare(ctx context.Context, s *Status) error {
	if err := b.capture(ctx, s); err != nil {
		return err
	}
	doc, err := composeDocument(s.PreviousCompose)
	if err != nil {
		return err
	}
	s.Manifest.Version = applianceTargetVersion
	s.Manifest.Images = map[string]string{}
	for name, service := range services(doc) {
		s.Manifest.Images[name] = service["image"].(string)
		service["restart"] = "no"
	}
	source := s.Manifest.Images["floatlab-control"]
	directory := filepath.Join(b.Dir, "candidate-"+s.OperationID)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	input, err := os.Open(filepath.Join(Directory, "appliance-management-candidate"))
	if err != nil {
		return fmt.Errorf("upload current candidate binary before running appliance tests: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(directory, "floatlab-control"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err = update.WriteFile(filepath.Join(directory, "Dockerfile"), []byte("FROM "+source+"\nCOPY floatlab-control /usr/local/bin/floatlab-control\nLABEL io.floatlab.appliance-candidate="+s.OperationID+"\n"), 0600); err != nil {
		return err
	}
	b.candidate = "floatlab-appliance-candidate:" + strings.ToLower(s.OperationID)
	if _, err = b.run(ctx, "docker", "build", "--pull=false", "-t", b.candidate, directory); err != nil {
		return err
	}
	s.Manifest.Images["floatlab-control"] = b.candidate
	control := services(doc)["floatlab-control"]
	control["image"] = b.candidate
	setEnvironment(control, "FLOATLAB_ACTIVATION_DIR", activationDir)
	s.CandidateCompose, err = yaml.Marshal(doc)
	return err
}
func (b *applianceFixture) Install(ctx context.Context, s *Status) error {
	if err := b.Linux.Install(ctx, s); err != nil {
		return err
	}
	check := *s
	check.Phase = "checking"
	if err := b.verifyRunningImages(ctx, &check); err != nil {
		return err
	}
	info, err := b.run(ctx, "docker", "exec", b.controlID(ctx), "/usr/local/bin/floatlab-control", "--release-info")
	if err != nil {
		return err
	}
	var candidate struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal([]byte(info), &candidate); err != nil {
		return err
	}
	if candidate.Version != applianceTargetVersion {
		return fmt.Errorf("candidate version %s, want %s", candidate.Version, applianceTargetVersion)
	}
	if err := os.WriteFile("/floatlab/system/management/rqlite/appliance-rollback-proof", []byte("candidate-wrote-this"), 0600); err != nil {
		return err
	}
	if b.failure == "crash" {
		os.Exit(86)
	}
	if b.failure == "install" || b.failure == "restore-crash" {
		return errors.New("injected production activation failure after real candidate startup")
	}
	return nil
}

func (b *applianceFixture) Restore(ctx context.Context, s *Status) error {
	if b.failure != "restore-crash" {
		return b.Linux.Restore(ctx, s)
	}
	restore := *b.Linux
	restore.Run = func(ctx context.Context, name string, args ...string) (string, error) {
		out, err := b.Linux.run(ctx, name, args...)
		if err == nil && name == "zfs" && len(args) > 0 && args[0] == "rollback" {
			os.Exit(87)
		}
		return out, err
	}
	return restore.Restore(ctx, s)
}

func requireAppliance(t *testing.T) {
	t.Helper()
	if os.Getenv("FLOATLAB_MANAGEMENT_APPLIANCE") != "1" {
		t.Skip("requires disposable appliance and explicit FLOATLAB_MANAGEMENT_APPLIANCE=1")
	}
	if os.Geteuid() != 0 {
		t.Fatal("appliance acceptance requires root")
	}
	environment, err := exec.Command("systemctl", "show", "-p", "Environment", "--value", "floatlab-hostd.service").Output()
	if err != nil {
		t.Fatal(err)
	}
	path := ""
	for _, field := range strings.Fields(string(environment)) {
		if value, ok := strings.CutPrefix(strings.Trim(field, "\""), "PATH="); ok {
			path = value
			break
		}
	}
	if path == "" {
		t.Fatal("hostd service PATH unavailable; cannot exercise production toolchain")
	}
	if err = os.Setenv("PATH", path); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"nft", "docker", "zfs", "systemctl"} {
		if _, err = exec.LookPath(name); err != nil {
			t.Fatalf("production hostd PATH missing %s: %v", name, err)
		}
	}
}

// The live developer ISO intentionally has no persistent identity. Bring this
// disposable fixture to the installed-appliance contract before testing updates.
func provisionApplianceIdentity(ctx context.Context, l *Linux) error {
	path := "/floatlab/system/etc/appliance.json"
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	raw, err := l.run(ctx, "docker", "inspect", "--format", "{{json .Config.Cmd}}", l.controlID(ctx))
	if err != nil {
		return err
	}
	var args []string
	if err = json.Unmarshal([]byte(raw), &args); err != nil {
		return err
	}
	nodeID := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "--host-node-id=") {
			nodeID = strings.TrimPrefix(arg, "--host-node-id=")
		}
	}
	if nodeID == "" {
		return errors.New("cannot derive developer appliance node identity")
	}
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	name, err := os.Hostname()
	if err != nil {
		return err
	}
	if err = update.WriteJSON(path, map[string]string{"node_id": nodeID, "node_name": name, "jwt_secret": hex.EncodeToString(secret), "timezone": "Etc/UTC"}); err != nil {
		return err
	}
	data, err := os.ReadFile(composePath)
	if err != nil {
		return err
	}
	doc, err := composeDocument(data)
	if err != nil {
		return err
	}
	setEnvironment(services(doc)["floatlab-control"], "FLOATLAB_APPLIANCE_CONFIG", path)
	data, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err = update.WriteFile(composePath, data, 0644); err != nil {
		return err
	}
	if err = l.compose(ctx, composePath, "floatlab-system", "up", "-d", "--no-deps", "--pull", "never", "floatlab-control"); err != nil {
		return err
	}
	// Wait for API authentication and identity registration to use the persisted identity.
	baseline := &Status{}
	deadline := time.Now().Add(time.Minute)
	for {
		if err = l.capture(ctx, baseline); err == nil {
			return l.Healthy(ctx, baseline)
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(time.Second)
	}
}
func applianceStatus(t *testing.T, m *Manager, id string, want string) *Status {
	t.Helper()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		status, err := m.Status(id)
		if err != nil {
			t.Fatal(err)
		}
		if terminal(status) {
			if status.State != want {
				t.Fatalf("transaction ended %s", applianceSummary(status))
			}
			if unlock, err := update.Acquire(m.Dir); err == nil {
				unlock()
				return status
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("transaction deadline: %s", applianceSummary(status))
		}
		time.Sleep(time.Second)
	}
}

func TestApplianceManagementTransactions(t *testing.T) {
	requireAppliance(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	l := &Linux{Dir: Directory}
	if err := provisionApplianceIdentity(ctx, l); err != nil {
		t.Fatal(err)
	}
	baseline := &Status{}
	if err := l.capture(ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.SourceRelease == applianceTargetRelease {
		t.Fatal("candidate must have a different version from source")
	}
	t.Logf("actual management transition %s -> %s", baseline.SourceRelease, applianceTargetRelease)
	source, err := composeDocument(baseline.PreviousCompose)
	if err != nil {
		t.Fatal(err)
	}
	sourceControl := services(source)["floatlab-control"]["image"].(string)
	// A separate workload and dataset must survive every management stop and rollback.
	workload := "floatlab-update-proof"
	if _, err = l.run(ctx, "docker", "run", "-d", "--name", workload, "--restart=unless-stopped", "--entrypoint", "/usr/bin/sleep", sourceControl, "infinity"); err != nil {
		t.Fatal(err)
	}
	defer l.run(context.Background(), "docker", "rm", "-f", workload)
	if _, err = l.run(ctx, "zfs", "create", "-p", "floatlab/stacks/update-proof"); err != nil {
		t.Fatal(err)
	}
	defer l.run(context.Background(), "zfs", "destroy", "floatlab/stacks/update-proof")
	if err = os.WriteFile("/floatlab/stacks/update-proof/marker", []byte("workload-data"), 0600); err != nil {
		t.Fatal(err)
	}
	workloadID, err := l.run(ctx, "docker", "inspect", "--format", "{{.Id}}", workload)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkload := func() {
		t.Helper()
		running, e := l.run(ctx, "docker", "inspect", "--format", "{{.Id}} {{.State.Running}} {{.HostConfig.RestartPolicy.Name}}", workload)
		if e != nil || running != workloadID+" true unless-stopped" {
			t.Fatalf("workload changed: %s %v", running, e)
		}
		data, e := os.ReadFile("/floatlab/stacks/update-proof/marker")
		if e != nil || string(data) != "workload-data" {
			t.Fatalf("workload data changed %q %v", data, e)
		}
	}
	for _, scenario := range []struct{ name, failure, want string }{{"preflight-failure", "preflight", "failed"}, {"commit", "", "succeeded"}, {"rollback", "install", "rolled_back"}} {
		if !t.Run(scenario.name, func(t *testing.T) {
			beforeControl := l.controlID(ctx)
			id := fmt.Sprintf("appliance-%s-%d", scenario.name, time.Now().Unix())
			backend := &applianceFixture{Linux: l, failure: scenario.failure}
			manager := &Manager{Dir: Directory, Backend: backend}
			if _, err := manager.Start(ctx, Request{OperationID: id, Release: applianceTargetRelease, Actor: "appliance-acceptance"}); err != nil {
				t.Fatal(err)
			}
			status := applianceStatus(t, manager, id, scenario.want)
			if scenario.failure == "preflight" {
				if !strings.Contains(status.Error, "injected failed internal self-test after real sandbox validation") {
					t.Fatalf("sandbox did not reach injected failure: %s", applianceSummary(status))
				}
				if status.Authorized || status.Snapshot != "" || l.controlID(ctx) != beforeControl {
					t.Fatalf("failed preflight changed production %s", applianceSummary(status))
				}
				assertWorkload()
				return
			}
			if !status.Authorized || status.Snapshot == "" {
				t.Fatalf("missing committed authorization/checkpoint %s", applianceSummary(status))
			}
			if scenario.failure != "" {
				if _, err := os.Stat("/floatlab/system/management/rqlite/appliance-rollback-proof"); err == nil {
					t.Fatal("candidate write survived rollback")
				}
			} else {
				if err := os.Remove("/floatlab/system/management/rqlite/appliance-rollback-proof"); err != nil {
					t.Fatal(err)
				}
			}
			assertWorkload()
			raw, err := l.run(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem", "-o", "name", "floatlab/system/update-tests")
			if err == nil && strings.Contains(raw, "/"+id) {
				t.Fatalf("test clone leaked %s", raw)
			}
			check := *status
			if scenario.want == "succeeded" {
				check.Phase = "checking"
			}
			if err = l.verifyRunningImages(ctx, &check); err != nil {
				t.Fatal(err)
			}
			// Return the successful candidate to its source so subsequent scenarios share a known baseline.
			if scenario.want == "succeeded" {
				if status, err = manager.Rollback(ctx, id); err != nil {
					t.Fatal(err)
				}
			}
			if err = l.Cleanup(ctx, status); err != nil {
				t.Fatal(err)
			}
			status.Snapshot = ""
			if err = manager.save(status); err != nil {
				t.Fatal(err)
			}
		}) {
			return
		}
	}
}

func TestApplianceManagementCrashAfterInstall(t *testing.T) {
	applianceCrash(t, "crash")
}

func TestApplianceManagementCrashDuringRestore(t *testing.T) { applianceCrash(t, "restore-crash") }

func applianceCrash(t *testing.T, failure string) {
	requireAppliance(t)
	l := &Linux{Dir: Directory}
	baseline := &Status{}
	if err := l.capture(context.Background(), baseline); err != nil {
		t.Fatal(err)
	}
	id := fmt.Sprintf("appliance-crash-%d", time.Now().Unix())
	if err := update.WriteJSON(filepath.Join(Directory, "appliance-crash-expected.json"), map[string]string{"operation_id": id, "source_release": baseline.SourceRelease}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Dir: Directory, Backend: &applianceFixture{Linux: l, failure: failure}}
	if _, err := manager.Start(context.Background(), Request{OperationID: id, Release: applianceTargetRelease, Actor: "appliance-acceptance"}); err != nil {
		t.Fatal(err)
	}
	applianceStatus(t, manager, id, "never")
	t.Fatal("fault injection did not terminate updater")
}

func TestApplianceManagementCrashRecovered(t *testing.T) {
	requireAppliance(t)
	b, err := os.ReadFile(filepath.Join(Directory, "appliance-crash-expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var expected map[string]string
	if err = json.Unmarshal(b, &expected); err != nil {
		t.Fatal(err)
	}
	m := NewDefault()
	// API readiness precedes the updater's stable-health authorization window.
	s := applianceStatus(t, m, expected["operation_id"], "rolled_back")
	if s.State != "rolled_back" || !s.Authorized || s.SourceRelease != expected["source_release"] {
		t.Fatalf("hostd failed recovery %s", applianceSummary(s))
	}
	if _, err = os.Stat("/floatlab/system/management/rqlite/appliance-rollback-proof"); err == nil {
		t.Fatal("crashed candidate write survived hostd rollback")
	}
	if err = m.Backend.(*Linux).verifyRunningImages(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	// Systemd may restart the generic launcher after Docker restarts. It must
	// recognize the exact stack already restored by hostd, rather than recreate
	// it with a different Compose override after recovery authorization.
	l := m.Backend.(*Linux)
	before, err := l.run(context.Background(), "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system")
	if err != nil || len(strings.Fields(before)) != 4 {
		t.Fatalf("recovered stack incomplete before generic startup: %q %v", before, err)
	}
	if _, err = l.run(context.Background(), "/run/current-system/sw/bin/floatlab-start-core-stack"); err != nil {
		t.Fatal(err)
	}
	after, err := l.run(context.Background(), "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system")
	if err != nil || after != before {
		t.Fatalf("generic startup changed recovered containers: before=%q after=%q error=%v", before, after, err)
	}
}

func applianceSummary(s *Status) string {
	return fmt.Sprintf("operation=%s state=%s phase=%s error=%q rollback_error=%q", s.OperationID, s.State, s.Phase, s.Error, s.RollbackError)
}
