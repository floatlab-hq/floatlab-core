package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/integrationvm"
	. "github.com/onsi/ginkgo/v2"
)

// This suite owns an installed UEFI VM. The development ISO's bare ZFS pool is insufficient.
func runHostUpdate(t testT, fixture *applianceFixture) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	source := filepath.Join(work, "source-host")
	candidate := filepath.Join(work, "candidate-host")
	bundle := filepath.Join(work, "bundle")
	isoResult := filepath.Join(work, "iso")
	flake := "path:" + root + "?dir=floatlab-appliance-image"
	iso := os.Getenv("FLOATLAB_HOST_TEST_ISO")
	if reuse := os.Getenv("FLOATLAB_HOST_TEST_CANDIDATE"); iso != "" || reuse != "" {
		if iso == "" || reuse == "" {
			t.Fatal("reuse requires both FLOATLAB_HOST_TEST_ISO and FLOATLAB_HOST_TEST_CANDIDATE")
		}
		candidate = reuse
	} else {
		run(t, root, []string{"FLOATLAB_INTEGRATION_COVERAGE=1", "FLOATLAB_HOST_VERSION=0.1.0-dev"}, "nix", "build", flake+"#host-image", "--impure", "--print-build-logs", "--out-link", source)
		run(t, root, []string{"FLOATLAB_INTEGRATION_COVERAGE=1"}, "python3", "scripts/release.py", "prepare-iso", bundle, "--version", "upgrade-acceptance", "--local-host", source)
		run(t, root, []string{"FLOATLAB_INTEGRATION_COVERAGE=1", "FLOATLAB_HOST_VERSION=0.1.0-dev", "FLOATLAB_ISO_BUNDLE=" + bundle}, "nix", "build", flake+"#iso", "--impure", "--print-build-logs", "--out-link", isoResult)
		run(t, root, []string{"FLOATLAB_INTEGRATION_COVERAGE=1", "FLOATLAB_HOST_VERSION=0.1.1-dev"}, "nix", "build", flake+"#host-image", "--impure", "--print-build-logs", "--out-link", candidate)
		images, err := filepath.Glob(filepath.Join(isoResult, "iso", "*.iso"))
		if err != nil || len(images) != 1 {
			t.Fatalf("expected one installer ISO: %v %v", images, err)
		}
		iso, err = filepath.EvalSymlinks(images[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	fixture.image = iso
	client := startApplianceWithAnswers(t, fixture, nil)
	vm := client.vm
	sshKey := hostSSHKey(t, vm)
	assertHostARCBudget(t, vm)
	hostBinary := filepath.Join(work, "host-tests")
	run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "test", "-c", "-cover", "-coverpkg=./...", "-covermode=atomic", "-tags=appliance,integrationcoverage", "-ldflags=-s -w", "-o", hostBinary, "./internal/hostupgrade")
	uploadUpgradeGuestFile(t, vm, hostBinary, "/floatlab/system/updates/appliance-host-tests")
	hostDownloadCandidate(t, vm, candidate)
	// Keep real workload data outside the management rollback tree throughout every host trial.
	if out, e := guestCommand(vm, `set -eu; zfs create -o mountpoint=/floatlab/host-qualification-workload floatlab/host-qualification-workload; docker image inspect alpine:3.20 >/dev/null 2>&1 || docker pull alpine:3.20; docker run -d --name floatlab-host-qualification-workload --restart unless-stopped -v /floatlab/host-qualification-workload:/data alpine:3.20 sh -ec 'test -f /data/marker || echo baseline > /data/marker; while :; do sleep 30; done'`); e != nil {
		t.Fatalf("create shared-engine workload fixture: %s %v", out, e)
	}
	for _, scenario := range []string{"firmware-fallback", "normal-fallback", "power-fallback", "heartbeat-fallback", "commit"} {
		By(scenario)
		func() {
			op := "host-qualification-" + scenario
			vars := []string{"FLOATLAB_HOST_TEST_OPERATION=" + op, "FLOATLAB_HOST_FIXTURE_DIR=/floatlab/system/updates/appliance-fixture"}
			if out, e := guestCommand(vm, `set -eu; printf before-preflight > /floatlab/system/management/raft/host-qualification.marker`); e != nil {
				t.Fatal(out, e)
			}
			hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceStage", op, vars, true)
			if out, e := guestCommand(vm, `set -eu; printf after-preflight > /floatlab/system/management/raft/host-qualification.marker`); e != nil {
				t.Fatal(out, e)
			}
			before := hostBootID(t, vm)
			if scenario == "firmware-fallback" {
				if out, e := guestCommand(vm, `set -eu; printf trial-workload > /floatlab/host-qualification-workload/marker; sync`); e != nil {
					t.Fatal(out, e)
				}
				hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceActivateBadEFI", op, vars, false)
				hostWaitBoot(t, vm, before, "recovered", sshKey, 15*time.Minute)
				hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceAssertRecovered", op, vars, true)
				if out, e := guestCommand(vm, `set -eu; test "$(cat /floatlab/host-qualification-workload/marker)" = trial-workload; test "$(docker inspect --format '{{.State.Running}}' floatlab-host-qualification-workload)" = true`); e != nil {
					t.Fatal(out, e)
				}
				hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCleanup", op, vars, true)
				return
			}
			hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceActivate", op, vars, false)
			hostWaitBoot(t, vm, before, "ready", sshKey, 15*time.Minute)
			hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceAssertTrial", op, vars, true)
			if out, e := guestCommand(vm, `set -eu; printf trial-write > /floatlab/system/management/raft/host-qualification.marker; printf trial-workload > /floatlab/host-qualification-workload/marker; sync`); e != nil {
				t.Fatal(out, e)
			}
			before = hostBootID(t, vm)
			switch scenario {
			case "normal-fallback":
				if out, e := guestCommand(vm, "systemd-run --on-active=2s systemctl reboot"); e != nil {
					t.Fatal(out, e)
				}
			case "power-fallback":
				checkpointGuest(t, vm)
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				err := vm.PowerCycle(ctx)
				cancel()
				if err != nil {
					t.Fatal(err)
				}
			case "heartbeat-fallback":
				if out, e := guestCommand(vm, "systemctl mask --runtime --now floatlab-hostd.service"); e != nil {
					t.Fatal(out, e)
				}
			case "commit":
				hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCommit", op, vars, true)
				if out, e := guestCommand(vm, "systemd-run --on-active=2s systemctl reboot"); e != nil {
					t.Fatal(out, e)
				}
			}
			state := "recovered"
			phase := "TestHostApplianceAssertRecovered"
			if scenario == "commit" {
				state = "committed"
				phase = "TestHostApplianceAssertCommitted"
			}
			hostWaitBoot(t, vm, before, state, sshKey, 15*time.Minute)
			hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", phase, op, vars, true)
			if out, e := guestCommand(vm, `set -eu; test "$(cat /floatlab/host-qualification-workload/marker)" = trial-workload; test "$(docker inspect --format '{{.State.Running}}' floatlab-host-qualification-workload)" = true; test "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' floatlab-host-qualification-workload)" = unless-stopped`); e != nil {
				t.Fatalf("OS fallback modified workload data or restart policy: %s %v", out, e)
			}
			hostGuestPhase(t, vm, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCleanup", op, vars, true)
		}()
		if t.Failed() {
			return
		}
	}
	t.Log("passed real installed-UEFI trial, pre-kernel/normal/power/heartbeat fallback, shared-engine workload persistence, and explicit permanent commitment; no published qualification was modified")
}

func assertHostARCBudget(t testT, vm *integrationvm.Guest) {
	t.Helper()
	out, err := guestCommand(vm, `set -eu; systemctl is-active floatlab-zfs-memory.service; limit=$(cat /sys/module/zfs/parameters/zfs_arc_max); test "$limit" -gt 0; test "$limit" -le 1073741824; printf 'ZFS ARC budget bytes=%s\n' "$limit"`)
	if err != nil {
		t.Fatalf("test appliance ARC budget: %s %v", out, err)
	}
	t.Log(out)
}

func hostGuestPhase(t testT, vm *integrationvm.Guest, binary, phase, operation string, variables []string, wait bool) {
	t.Helper()
	directory := "/floatlab/system/updates"
	prefix := directory + "/acceptance-" + operation + "-" + phase
	environment := "env FLOATLAB_APPLIANCE_TEST=1"
	for _, v := range variables {
		environment += " " + hostShellQuote(v)
	}
	script := fmt.Sprintf("rm -f %[1]s.exit %[1]s.json; coverage_dir=/floatlab/system/integration-coverage/guest-$(basename %[1]s)-$(date +%%s%%N); mkdir -p \"$coverage_dir\"; (GOCOVERDIR=\"$coverage_dir\" %[2]s %[3]s -test.gocoverdir=\"$coverage_dir\" -test.run='^TestHostApplianceIntegration$' -ginkgo.focus='%[4]s$' -ginkgo.json-report=%[1]s.json -test.v -test.timeout=25m >%[1]s.log 2>&1; echo $? >%[1]s.exit) </dev/null >/dev/null 2>&1 & echo started", prefix, environment, binary, phase)
	if out, err := guestCommand(vm, script); err != nil {
		t.Fatalf("start %s: %s %v", phase, out, err)
	}
	if !wait {
		return
	}
	waitFor(t, 27*time.Minute, func() error {
		out, err := guestCommand(vm, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) == "running" {
			return fmt.Errorf("%s still running", phase)
		}
		logs, e := guestCommand(vm, "cat "+prefix+".log")
		t.Logf("%s:\n%s", phase, logs)
		if e != nil {
			t.Fatal(e)
		}
		if strings.TrimSpace(out) != "0" {
			t.Fatalf("%s failed with exit %s", phase, out)
		}
		verifyGuestReport(t, vm, prefix+".json")
		return nil
	})
}
func hostShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func hostBootID(t testT, vm *integrationvm.Guest) string {
	t.Helper()
	out, err := guestCommand(vm, "cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}
func hostSSHKey(t testT, vm *integrationvm.Guest) string {
	t.Helper()
	out, err := guestCommand(vm, "cat /etc/ssh/ssh_host_ed25519_key.pub")
	fields := strings.Fields(out)
	if err != nil || len(fields) < 2 || fields[0] != "ssh-ed25519" {
		t.Fatalf("read appliance SSH host identity: %s %v", out, err)
	}
	return strings.Join(fields[:2], " ")
}

func hostWaitBoot(t testT, vm *integrationvm.Guest, before, state, sshKey string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() error {
		script := "set -eu; op=$(jq -r .operation_id /floatlab/system/updates/host.json); for phase in TestHostApplianceActivate TestHostApplianceActivateBadEFI; do result=/floatlab/system/updates/acceptance-$op-$phase; if [ -f \"$result.exit\" ] && [ \"$(cat \"$result.exit\")\" != 0 ]; then printf 'HOST_ACTIVATION_FAILED\\n'; cat \"$result.log\"; exit 0; fi; done; test \"$(cat /proc/sys/kernel/random/boot_id)\" != " + hostShellQuote(before) + "; test \"$(jq -r .state /floatlab/system/updates/host.json)\" = " + hostShellQuote(state) + "; curl -fsS http://127.0.0.1:8080/api/v1/health/ready; systemctl is-active --quiet floatlab-core-stack.service; flock -n /floatlab/system/updates/update.lock true"
		out, err := guestCommand(vm, script)
		if strings.HasPrefix(out, "HOST_ACTIVATION_FAILED\n") {
			t.Fatalf("activation helper failed before reboot:\n%s", strings.TrimPrefix(out, "HOST_ACTIVATION_FAILED\n"))
		}
		if err != nil {
			return fmt.Errorf("waiting for %s boot: %s %w", state, out, err)
		}
		return nil
	})
	if hostSSHKey(t, vm) != sshKey {
		t.Fatal("SSH host identity changed across installed OS reboot")
	}
}

func hostDownloadCandidate(t testT, vm *integrationvm.Guest, directory string) {
	t.Helper()
	gateway := vm.Run.Gateway
	listener, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.FileServer(http.Dir(directory)), ReadHeaderTimeout: 10 * time.Second}
	go server.Serve(listener)
	defer server.Shutdown(context.Background())
	address := "http://" + listener.Addr().String()
	prefix := "/floatlab/system/updates/appliance-download"
	script := fmt.Sprintf("mkdir -p /floatlab/system/updates/appliance-fixture; (curl -f --retry 3 %s/host-manifest.json -o /floatlab/system/updates/appliance-fixture/host-manifest.json && curl -f --retry 3 %s/floatlab-root.tar.gz -o /floatlab/system/updates/appliance-fixture/floatlab-root.tar.gz; echo $? > %s.exit) >%s.log 2>&1 </dev/null & echo started", address, address, prefix, prefix)
	if out, e := guestCommand(vm, script); e != nil {
		t.Fatal(out, e)
	}
	waitFor(t, 10*time.Minute, func() error {
		out, e := guestCommand(vm, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
		if e != nil {
			return e
		}
		if strings.TrimSpace(out) == "running" {
			return fmt.Errorf("candidate download running")
		}
		if strings.TrimSpace(out) != "0" {
			logs, _ := guestCommand(vm, "cat "+prefix+".log")
			t.Fatalf("candidate transfer failed: %s %s", out, logs)
		}
		return nil
	})
	var manifest struct {
		Files map[string]string `json:"files"`
	}
	data, err := os.ReadFile(filepath.Join(directory, "host-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if out, e := guestCommand(vm, "echo "+hostShellQuote(manifest.Files["floatlab-root.tar.gz"]+"  /floatlab/system/updates/appliance-fixture/floatlab-root.tar.gz")+" | sha256sum --check"); e != nil {
		t.Fatalf("candidate transfer integrity: %s %v", out, e)
	}
}
