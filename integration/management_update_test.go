package integration_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/integrationvm"
)

func (c *apiClient) assertManagementUpdateAppliance() {
	t := c.t
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "management-appliance-tests")
	candidateBinary := filepath.Join(t.TempDir(), "floatlab-control-candidate")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-cover", "-coverpkg=./...", "-covermode=atomic", "-tags=integrationcoverage", "-ldflags=-s -w -X github.com/floatlab/floatlab-core/pkg/release.Version=0.1.1-dev", "-o", candidateBinary, "./cmd/floatlab-control")
	uploadUpgradeGuestFile(t, c.vm, candidateBinary, "/floatlab/system/updates/appliance-management-candidate")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "test", "-c", "-cover", "-coverpkg=./...", "-covermode=atomic", "-tags=appliance,integrationcoverage", "-ldflags=-s -w", "-o", binary, "./internal/managementupdate")
	remote := "/floatlab/system/updates/appliance-management-tests"
	uploadUpgradeGuestFile(t, c.vm, binary, remote)
	installCurrentUpgradeHostd(t, c.vm)
	c.waitReady()
	c.login()
	runGuestTest := func(name string, wantExit int) {
		t.Helper()
		prefix := "/floatlab/system/updates/acceptance-" + name
		script := fmt.Sprintf("rm -f %[1]s.exit %[1]s.json; coverage_dir=/floatlab/system/integration-coverage/guest-$(basename %[1]s)-$(date +%%s%%N); mkdir -p \"$coverage_dir\"; (GOCOVERDIR=\"$coverage_dir\" FLOATLAB_MANAGEMENT_APPLIANCE=1 %[2]s -test.gocoverdir=\"$coverage_dir\" -test.run='^TestManagementApplianceIntegration$' -ginkgo.focus='%[3]s$' -ginkgo.json-report=%[1]s.json -test.v -test.timeout=20m >%[1]s.log 2>&1; echo $? >%[1]s.exit) </dev/null >/dev/null 2>&1 & echo started", prefix, remote, name)
		if out, err := guestCommand(c.vm, script); err != nil {
			t.Fatalf("start guest test: %s %v", out, err)
		}
		waitFor(t, 22*time.Minute, func() error {
			out, err := guestCommand(c.vm, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) == "running" {
				return fmt.Errorf("guest test %s running", name)
			}
			logs, logErr := guestCommand(c.vm, "cat "+prefix+".log")
			t.Logf("%s guest output:\n%s", name, logs)
			if logErr != nil {
				t.Fatal(logErr)
			}
			if strings.TrimSpace(out) != fmt.Sprint(wantExit) {
				t.Fatalf("guest test %s exited %q, want %d", name, out, wantExit)
			}
			if wantExit == 0 {
				verifyGuestReport(t, c.vm, prefix+".json")
			}
			return nil
		})
	}
	runGuestTest("TestApplianceSandboxFirewallParser", 0)
	runGuestTest("TestApplianceManagementTransactions", 0)
	c.waitReady()
	c.login()
	nodes := c.expect("GET", "/api/v1/nodes", nil, 200).([]interface{})
	if len(nodes) != 1 {
		t.Fatalf("expected standalone node, got %+v", nodes)
	}
	nodeID := nodes[0].(map[string]interface{})["id"].(string)
	status := c.object("GET", "/api/v1/nodes/"+nodeID+"/updates/management/status", nil, 200)
	if status["operation_id"] == "" {
		t.Fatal("hostd did not expose actual upgrade status")
	}
	runGuestTest("TestApplianceManagementCrashAfterInstall", 86)
	// Docker must not restart the authorized-but-uncommitted candidate itself.
	out, err := guestCommand(c.vm, `set -e; docker ps -aq --filter label=com.docker.compose.project=floatlab-system | while read -r id; do test "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' "$id")" = no || exit 1; done; systemctl restart docker.service; test -z "$(docker ps -q --filter label=com.docker.compose.project=floatlab-system)"; systemctl restart floatlab-hostd.service`)
	if err != nil {
		t.Fatalf("restart-policy suppression / hostd restart: %s %v", out, err)
	}
	c.waitReady()
	c.login()
	runGuestTest("TestApplianceManagementCrashRecovered", 0)
	runGuestTest("TestApplianceManagementCrashDuringRestore", 87)
	if out, err := guestCommand(c.vm, "systemctl restart floatlab-hostd.service"); err != nil {
		t.Fatalf("restart hostd during partial restore: %s %v", out, err)
	}
	c.waitReady()
	c.login()
	runGuestTest("TestApplianceManagementCrashRecovered", 0)
	// Exercise the authenticated public projection and cleanup against the real hostd journal.
	releases := c.expect("GET", "/api/v1/nodes/"+nodeID+"/updates/management/releases", nil, 200).([]interface{})
	if len(releases) == 0 {
		t.Fatal("official management release discovery returned no releases")
	}
	status = c.object("GET", "/api/v1/nodes/"+nodeID+"/updates/management/status", nil, 200)
	operationID, ok := status["operation_id"].(string)
	if !ok || operationID == "" {
		t.Fatalf("missing recovered operation %+v", status)
	}
	c.object("POST", "/api/v1/nodes/"+nodeID+"/updates/management/cleanup", map[string]string{"operation_id": operationID}, 202)
	waitFor(t, time.Minute, func() error {
		current := c.object("GET", "/api/v1/nodes/"+nodeID+"/updates/management/status?operation_id="+operationID, nil, 200)
		if current["phase"] == "cleaned" {
			if current["state"] != "rolled_back" {
				t.Fatalf("cleanup hid rollback outcome %+v", current)
			}
			return nil
		}
		return fmt.Errorf("cleanup phase %v", current["phase"])
	})
}

// Upload through the existing guest agent so disposable VMs need no SSH credentials.
func uploadUpgradeGuestFile(t testT, vm *integrationvm.Guest, source, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	if err := vm.Upload(ctx, source, target); err != nil {
		t.Fatal(err)
	}
}

func installCurrentUpgradeHostd(t testT, vm *integrationvm.Guest) {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	hostdBinary := filepath.Join(t.TempDir(), "floatlab-hostd-current")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-cover", "-coverpkg=./...", "-covermode=atomic", "-tags=integrationcoverage", "-ldflags=-s -w", "-o", hostdBinary, "./cmd/floatlab-hostd")
	uploadUpgradeGuestFile(t, vm, hostdBinary, "/floatlab/system/updates/appliance-hostd-current")
	if out, err := guestCommand(vm, `mkdir -p /run/systemd/system/floatlab-hostd.service.d; printf '[Service]\nExecStart=\nExecStart=/floatlab/system/updates/appliance-hostd-current\n' >/run/systemd/system/floatlab-hostd.service.d/acceptance.conf; systemctl daemon-reload; systemctl restart floatlab-hostd.service`); err != nil {
		t.Fatalf("start current hostd acceptance binary: %s %v", out, err)
	}
}
