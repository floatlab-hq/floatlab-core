package integration_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagementUpdateAppliance(t *testing.T) {
	if os.Getenv("FLOATLAB_UPDATE_INTEGRATION") != "1" {
		t.Skip("set FLOATLAB_UPDATE_INTEGRATION=1 for destructive disposable-appliance upgrade tests")
	}
	if os.Getenv("API_URL") == "" {
		t.Setenv("VM_NAME", fmt.Sprintf("floatlab-management-upgrade-%d", time.Now().UnixNano()))
	}
	c := newAPIClient(t, startAppliance(t))
	c.waitReady()
	c.login()
	c.waitNodeReady()
	c.assertHostNetworkTransactions()
	c.assertManagementUpdateAppliance()
}

func TestManagementUpdateRecoverRetainedAppliance(t *testing.T) {
	if os.Getenv("FLOATLAB_UPDATE_INTEGRATION") != "1" || os.Getenv("API_URL") == "" || os.Getenv("VM_NAME") == "" {
		t.Skip("requires explicit kept disposable VM API_URL/VM_NAME and update opt-in")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	installCurrentUpgradeHostd(t, root)
	c := newAPIClient(t, os.Getenv("API_URL"))
	c.waitReady()
	c.login()
	c.waitNodeReady()
	nodes := c.expect("GET", "/api/v1/nodes", nil, 200).([]interface{})
	if len(nodes) != 1 {
		t.Fatal("expected standalone appliance")
	}
	id := nodes[0].(map[string]interface{})["id"].(string)
	waitFor(t, 3*time.Minute, func() error {
		s := c.object("GET", "/api/v1/nodes/"+id+"/updates/management/status", nil, 200)
		if s["state"] == "rolled_back" {
			return nil
		}
		if s["state"] == "recovery_failed" {
			t.Fatalf("retained checkpoint recovery failed: state=%v phase=%v error=%v rollback_error=%v", s["state"], s["phase"], s["error"], s["rollback_error"])
		}
		return fmt.Errorf("recovery phase=%v state=%v", s["phase"], s["state"])
	})
}

func (c *apiClient) assertManagementUpdateAppliance() {
	t := c.t
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "management-appliance-tests")
	candidateBinary := filepath.Join(t.TempDir(), "floatlab-control-candidate")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-ldflags=-s -w -X github.com/floatlab/floatlab-core/pkg/release.Version=0.1.1-dev", "-o", candidateBinary, "./cmd/floatlab-control")
	uploadUpgradeGuestFile(t, root, candidateBinary, "/floatlab/system/updates/appliance-management-candidate")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "test", "-c", "-tags=appliance", "-ldflags=-s -w", "-o", binary, "./internal/managementupdate")
	remote := "/floatlab/system/updates/appliance-management-tests"
	uploadUpgradeGuestFile(t, root, binary, remote)
	installCurrentUpgradeHostd(t, root)
	c.waitReady()
	c.login()
	runGuestTest := func(name string, wantExit int) {
		t.Helper()
		prefix := "/floatlab/system/updates/acceptance-" + name
		script := fmt.Sprintf("rm -f %[1]s.exit; (FLOATLAB_MANAGEMENT_APPLIANCE=1 %[2]s -test.run='^%[3]s$' -test.v -test.timeout=20m >%[1]s.log 2>&1; echo $? >%[1]s.exit) </dev/null >/dev/null 2>&1 & echo started", prefix, remote, name)
		if out, err := guestCommand(root, script); err != nil {
			t.Fatalf("start guest test: %s %v", out, err)
		}
		waitFor(t, 22*time.Minute, func() error {
			out, err := guestCommand(root, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) == "running" {
				return fmt.Errorf("guest test %s running", name)
			}
			logs, logErr := guestCommand(root, "cat "+prefix+".log")
			t.Logf("%s guest output:\n%s", name, logs)
			if logErr != nil {
				t.Fatal(logErr)
			}
			if strings.TrimSpace(out) != fmt.Sprint(wantExit) {
				t.Fatalf("guest test %s exited %q, want %d", name, out, wantExit)
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
	out, err := guestCommand(root, `set -e; docker ps -aq --filter label=com.docker.compose.project=floatlab-system | while read -r id; do test "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' "$id")" = no || exit 1; done; systemctl restart docker.service; test -z "$(docker ps -q --filter label=com.docker.compose.project=floatlab-system)"; systemctl restart floatlab-hostd.service`)
	if err != nil {
		t.Fatalf("restart-policy suppression / hostd restart: %s %v", out, err)
	}
	c.waitReady()
	c.login()
	runGuestTest("TestApplianceManagementCrashRecovered", 0)
	runGuestTest("TestApplianceManagementCrashDuringRestore", 87)
	if out, err := guestCommand(root, "systemctl restart floatlab-hostd.service"); err != nil {
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
func uploadUpgradeGuestFile(t *testing.T, root, source, target string) {
	t.Helper()
	uploadTarget := target + ".upload"
	if ssh := os.Getenv("VM_SSH"); ssh != "" {
		temporary := "/tmp/" + filepath.Base(target)
		run(t, root, nil, "scp", "-o", "BatchMode=yes", source, ssh+":"+temporary)
		if out, err := guestCommand(root, "install -m 0700 "+temporary+" "+uploadTarget+"; mv -f "+uploadTarget+" "+target+"; rm -f "+temporary); err != nil {
			t.Fatalf("install guest helper: %s %v", out, err)
		}
		return
	}
	uri, name := env("LIBVIRT_URI", "qemu:///system"), env("VM_NAME", "floatlab-integration")
	agent := func(commandName string, args map[string]any) json.RawMessage {
		t.Helper()
		request, _ := json.Marshal(map[string]any{"execute": commandName, "arguments": args})
		out, err := command(20*time.Second, root, nil, "virsh", "-c", uri, "qemu-agent-command", name, string(request))
		if err != nil {
			t.Fatalf("%s: %s %v", commandName, out, err)
		}
		var result struct {
			Return json.RawMessage `json:"return"`
			Error  any             `json:"error"`
		}
		if err = json.Unmarshal([]byte(out), &result); err != nil || result.Error != nil {
			t.Fatalf("%s response %s: %v", commandName, out, err)
		}
		return result.Return
	}
	if out, err := guestCommand(root, "mkdir -p "+filepath.Dir(target)); err != nil {
		t.Fatalf("mkdir helper directory: %s %v", out, err)
	}
	var handle int
	if err := json.Unmarshal(agent("guest-file-open", map[string]any{"path": uploadTarget, "mode": "w"}), &handle); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			var written struct {
				Count int `json:"count"`
			}
			if err = json.Unmarshal(agent("guest-file-write", map[string]any{"handle": handle, "buf-b64": base64.StdEncoding.EncodeToString(buffer[:n])}), &written); err != nil || written.Count != n {
				t.Fatalf("short guest file write: %+v %v", written, err)
			}
		}
		if readErr != nil {
			if readErr != io.EOF {
				t.Fatal(readErr)
			}
			break
		}
	}
	agent("guest-file-close", map[string]any{"handle": handle})
	if out, err := guestCommand(root, "chmod 0700 "+uploadTarget+"; mv -f "+uploadTarget+" "+target); err != nil {
		t.Fatalf("chmod helper: %s %v", out, err)
	}
}

func installCurrentUpgradeHostd(t *testing.T, root string) {
	t.Helper()
	hostdBinary := filepath.Join(t.TempDir(), "floatlab-hostd-current")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "build", "-ldflags=-s -w", "-o", hostdBinary, "./cmd/floatlab-hostd")
	uploadUpgradeGuestFile(t, root, hostdBinary, "/floatlab/system/updates/appliance-hostd-current")
	if out, err := guestCommand(root, `mkdir -p /run/systemd/system/floatlab-hostd.service.d; printf '[Service]\nExecStart=\nExecStart=/floatlab/system/updates/appliance-hostd-current\n' >/run/systemd/system/floatlab-hostd.service.d/acceptance.conf; systemctl daemon-reload; systemctl restart floatlab-hostd.service`); err != nil {
		t.Fatalf("start current hostd acceptance binary: %s %v", out, err)
	}
}
