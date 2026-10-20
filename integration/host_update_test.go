package integration_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This suite owns an installed UEFI VM. The development ISO's bare ZFS pool is insufficient.
func TestHostUpdateAppliance(t *testing.T) {
	if os.Getenv("FLOATLAB_HOST_UPDATE_INTEGRATION") != "1" {
		t.Skip("set FLOATLAB_HOST_UPDATE_INTEGRATION=1 for installed UEFI upgrade acceptance")
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	uri, pool := env("LIBVIRT_URI", "qemu:///system"), env("LIBVIRT_POOL", "default")
	name := fmt.Sprintf("floatlab-host-upgrade-%d", time.Now().UnixNano())
	volume := name + ".qcow2"
	t.Setenv("VM_NAME", name)
	t.Setenv("VM_SSH", "")
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
		run(t, root, []string{"FLOATLAB_HOST_VERSION=0.1.0-dev"}, "nix", "build", flake+"#host-image", "--impure", "--print-build-logs", "--out-link", source)
		run(t, root, nil, "python3", "scripts/release.py", "prepare-iso", bundle, "--version", "upgrade-acceptance", "--local-host", source)
		run(t, root, []string{"FLOATLAB_HOST_VERSION=0.1.0-dev", "FLOATLAB_ISO_BUNDLE=" + bundle}, "nix", "build", flake+"#iso", "--impure", "--print-build-logs", "--out-link", isoResult)
		run(t, root, []string{"FLOATLAB_HOST_VERSION=0.1.1-dev"}, "nix", "build", flake+"#host-image", "--impure", "--print-build-logs", "--out-link", candidate)
		images, err := filepath.Glob(filepath.Join(isoResult, "iso", "*.iso"))
		if err != nil || len(images) != 1 {
			t.Fatalf("expected one installer ISO: %v %v", images, err)
		}
		iso, err = filepath.EvalSymlinks(images[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	beforePools := map[string]bool{}
	raw, err := command(time.Minute, root, nil, "virsh", "-c", uri, "pool-list", "--all", "--name")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range strings.Fields(raw) {
		beforePools[entry] = true
	}
	t.Cleanup(func() {
		if os.Getenv("FLOATLAB_KEEP_VM") == "1" {
			t.Logf("keeping installed qualification VM %s", name)
			return
		}
		command(time.Minute, root, nil, "virsh", "-c", uri, "destroy", name)
		command(time.Minute, root, nil, "virsh", "-c", uri, "undefine", name, "--nvram")
		command(time.Minute, root, nil, "virsh", "-c", uri, "vol-delete", "--pool", pool, volume)
		cleanupISOPools(t, root, uri, filepath.Dir(iso), beforePools)
	})
	if _, err = command(time.Minute, root, nil, "virsh", "-c", uri, "dominfo", name); err == nil {
		t.Fatal("refusing to replace an existing VM")
	}
	run(t, root, nil, "virsh", "-c", uri, "vol-create-as", pool, volume, "64G", "--format", "qcow2", "--allocation", "0")
	run(t, root, nil, "virt-install", "--connect", uri, "--name", name, "--memory", "16384", "--vcpus", "4", "--cpu", "host-passthrough", "--import", "--wait", "0", "--osinfo", "detect=on,require=off", "--boot", "uefi", "--disk", "path="+iso+",device=cdrom,readonly=on,boot_order=1", "--disk", "vol="+pool+"/"+volume+",bus=virtio,serial=floatlab-upgrade,boot_order=2", "--network", "network=default,model=virtio", "--channel", "unix,target_type=virtio,name=org.qemu.guest_agent.0", "--graphics", "none", "--console", "pty,target_type=serial", "--noautoconsole")
	t.Cleanup(func() {
		if t.Failed() {
			out, e := guestCommand(root, `cat /floatlab/system/updates/host.json /run/floatlab-installer/status.json /floatlab/system/etc/install-status.json /run/floatlab-upgrade-acceptance/acceptance-install-TestApplianceInstall.log 2>/dev/null || true; journalctl -u floatlab-installer-console@ttyS0 -u floatlab-installer-root -u floatlab-hostd -u floatlab-boot-recovery -u floatlab-core-stack -u docker --no-pager -n 200 || true; zfs list -t all || true; efibootmgr -v || true`)
			t.Logf("installed upgrade diagnostics: %s %v", out, e)
		}
	})
	waitFor(t, 10*time.Minute, func() error { _, e := guestCommand(root, "true"); return e })
	installerBinary := filepath.Join(work, "installer-tests")
	hostBinary := filepath.Join(work, "host-tests")
	for _, entry := range []struct{ pkg, binary string }{{"./internal/installer", installerBinary}, {"./internal/hostupgrade", hostBinary}} {
		run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "test", "-c", "-tags=appliance", "-ldflags=-s -w", "-o", entry.binary, entry.pkg)
	}
	if out, e := guestCommand(root, "set -eu; systemctl stop floatlab-installer-display.service floatlab-installer-console@ttyS0.service; mkdir -p /run/floatlab-upgrade-acceptance"); e != nil {
		t.Fatalf("stop interactive installer: %s %v", out, e)
	}
	uploadUpgradeGuestFile(t, root, installerBinary, "/run/floatlab-upgrade-acceptance/install-tests")
	hostGuestPhase(t, root, "/run/floatlab-upgrade-acceptance/install-tests", "TestApplianceInstall", "install", nil, true)
	// Shut down the temporary installer root cleanly before removing its mounted media.
	if out, e := guestCommand(root, `set -eu; systemd-run --on-active=2s sh -ec 'systemctl stop floatlab-installer-root.service; zpool export floatlab; systemctl poweroff'`); e != nil {
		t.Fatalf("poweroff installer: %s %v", out, e)
	}
	waitFor(t, 5*time.Minute, func() error {
		out, e := command(time.Minute, root, nil, "virsh", "-c", uri, "domstate", name)
		if e != nil {
			return e
		}
		if strings.TrimSpace(out) != "shut off" {
			return fmt.Errorf("installer still running: %s", out)
		}
		return nil
	})
	domain, err := command(time.Minute, root, nil, "virsh", "-c", uri, "dumpxml", name)
	if err != nil {
		t.Fatal(err)
	}
	var domainXML struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"devices>disk"`
	}
	if err = xml.Unmarshal([]byte(domain), &domainXML); err != nil {
		t.Fatal(err)
	}
	cdrom := ""
	for _, disk := range domainXML.Disks {
		if disk.Device == "cdrom" {
			cdrom = disk.Target.Dev
		}
	}
	if cdrom == "" {
		t.Fatal("installer CD device not found")
	}
	run(t, root, nil, "virsh", "-c", uri, "change-media", name, cdrom, "--eject", "--config", "--force")
	run(t, root, nil, "virsh", "-c", uri, "start", name)
	waitFor(t, 10*time.Minute, func() error {
		out, e := guestCommand(root, "set -eu; test \"$(findmnt -n -o SOURCE /)\" = floatlab/os/live; curl -fsS http://127.0.0.1:8080/api/v1/health/ready")
		if e != nil {
			return fmt.Errorf("installed root readiness: %s %w", out, e)
		}
		return nil
	})
	uploadUpgradeGuestFile(t, root, hostBinary, "/floatlab/system/updates/appliance-host-tests")
	hostDownloadCandidate(t, root, uri, candidate)
	// Keep real workload data outside the management rollback tree throughout every host trial.
	if out, e := guestCommand(root, `set -eu; zfs create -o mountpoint=/floatlab/host-qualification-workload floatlab/host-qualification-workload; docker image inspect alpine:3.20 >/dev/null 2>&1 || docker pull alpine:3.20; docker run -d --name floatlab-host-qualification-workload --restart unless-stopped -v /floatlab/host-qualification-workload:/data alpine:3.20 sh -ec 'test -f /data/marker || echo baseline > /data/marker; while :; do sleep 30; done'`); e != nil {
		t.Fatalf("create shared-engine workload fixture: %s %v", out, e)
	}
	for _, scenario := range []string{"firmware-fallback", "normal-fallback", "power-fallback", "heartbeat-fallback", "commit"} {
		t.Run(scenario, func(t *testing.T) {
			op := "host-qualification-" + scenario
			vars := []string{"FLOATLAB_HOST_TEST_OPERATION=" + op, "FLOATLAB_HOST_FIXTURE_DIR=/floatlab/system/updates/appliance-fixture"}
			if out, e := guestCommand(root, `set -eu; printf before-preflight > /floatlab/system/management/raft/host-qualification.marker`); e != nil {
				t.Fatal(out, e)
			}
			hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceStage", op, vars, true)
			if out, e := guestCommand(root, `set -eu; printf after-preflight > /floatlab/system/management/raft/host-qualification.marker`); e != nil {
				t.Fatal(out, e)
			}
			before := hostBootID(t, root)
			if scenario == "firmware-fallback" {
				if out, e := guestCommand(root, `set -eu; printf trial-workload > /floatlab/host-qualification-workload/marker; sync`); e != nil {
					t.Fatal(out, e)
				}
				hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceActivateBadEFI", op, vars, false)
				hostWaitBoot(t, root, before, "recovered", 15*time.Minute)
				hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceAssertRecovered", op, vars, true)
				if out, e := guestCommand(root, `set -eu; test "$(cat /floatlab/host-qualification-workload/marker)" = trial-workload; test "$(docker inspect --format '{{.State.Running}}' floatlab-host-qualification-workload)" = true`); e != nil {
					t.Fatal(out, e)
				}
				hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCleanup", op, vars, true)
				return
			}
			hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceActivate", op, vars, false)
			hostWaitBoot(t, root, before, "ready", 15*time.Minute)
			hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceAssertTrial", op, vars, true)
			if out, e := guestCommand(root, `set -eu; printf trial-write > /floatlab/system/management/raft/host-qualification.marker; printf trial-workload > /floatlab/host-qualification-workload/marker; sync`); e != nil {
				t.Fatal(out, e)
			}
			before = hostBootID(t, root)
			switch scenario {
			case "normal-fallback":
				if out, e := guestCommand(root, "systemd-run --on-active=2s systemctl reboot"); e != nil {
					t.Fatal(out, e)
				}
			case "power-fallback":
				run(t, root, nil, "virsh", "-c", uri, "destroy", name)
				run(t, root, nil, "virsh", "-c", uri, "start", name)
			case "heartbeat-fallback":
				if out, e := guestCommand(root, "systemctl mask --runtime --now floatlab-hostd.service"); e != nil {
					t.Fatal(out, e)
				}
			case "commit":
				hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCommit", op, vars, true)
				if out, e := guestCommand(root, "systemd-run --on-active=2s systemctl reboot"); e != nil {
					t.Fatal(out, e)
				}
			}
			state := "recovered"
			phase := "TestHostApplianceAssertRecovered"
			if scenario == "commit" {
				state = "committed"
				phase = "TestHostApplianceAssertCommitted"
			}
			hostWaitBoot(t, root, before, state, 15*time.Minute)
			hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", phase, op, vars, true)
			if out, e := guestCommand(root, `set -eu; test "$(cat /floatlab/host-qualification-workload/marker)" = trial-workload; test "$(docker inspect --format '{{.State.Running}}' floatlab-host-qualification-workload)" = true; test "$(docker inspect --format '{{.HostConfig.RestartPolicy.Name}}' floatlab-host-qualification-workload)" = unless-stopped`); e != nil {
				t.Fatalf("OS fallback modified workload data or restart policy: %s %v", out, e)
			}
			hostGuestPhase(t, root, "/floatlab/system/updates/appliance-host-tests", "TestHostApplianceCleanup", op, vars, true)
		})
		if t.Failed() {
			return
		}
	}
	t.Log("passed real installed-UEFI trial, pre-kernel/normal/power/heartbeat fallback, shared-engine workload persistence, and explicit permanent commitment; no published qualification was modified")
}

func hostGuestPhase(t *testing.T, root, binary, phase, operation string, variables []string, wait bool) {
	t.Helper()
	directory := "/floatlab/system/updates"
	if phase == "TestApplianceInstall" {
		directory = "/run/floatlab-upgrade-acceptance"
	}
	prefix := directory + "/acceptance-" + operation + "-" + phase
	environment := "env FLOATLAB_APPLIANCE_TEST=1"
	for _, v := range variables {
		environment += " " + hostShellQuote(v)
	}
	script := fmt.Sprintf("rm -f %[1]s.exit; (%[2]s %[3]s -test.run='^%[4]s$' -test.v -test.timeout=25m >%[1]s.log 2>&1; echo $? >%[1]s.exit) </dev/null >/dev/null 2>&1 & echo started", prefix, environment, binary, phase)
	if out, err := guestCommand(root, script); err != nil {
		t.Fatalf("start %s: %s %v", phase, out, err)
	}
	if !wait {
		return
	}
	waitFor(t, 27*time.Minute, func() error {
		out, err := guestCommand(root, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
		if err != nil {
			return err
		}
		if strings.TrimSpace(out) == "running" {
			return fmt.Errorf("%s still running", phase)
		}
		logs, e := guestCommand(root, "cat "+prefix+".log")
		t.Logf("%s:\n%s", phase, logs)
		if e != nil {
			t.Fatal(e)
		}
		if strings.TrimSpace(out) != "0" {
			t.Fatalf("%s failed with exit %s", phase, out)
		}
		return nil
	})
}
func hostShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
func hostBootID(t *testing.T, root string) string {
	t.Helper()
	out, err := guestCommand(root, "cat /proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}
func hostWaitBoot(t *testing.T, root, before, state string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, func() error {
		script := "set -eu; op=$(jq -r .operation_id /floatlab/system/updates/host.json); for phase in TestHostApplianceActivate TestHostApplianceActivateBadEFI; do result=/floatlab/system/updates/acceptance-$op-$phase; if [ -f \"$result.exit\" ] && [ \"$(cat \"$result.exit\")\" != 0 ]; then printf 'HOST_ACTIVATION_FAILED\\n'; cat \"$result.log\"; exit 0; fi; done; test \"$(cat /proc/sys/kernel/random/boot_id)\" != " + hostShellQuote(before) + "; test \"$(jq -r .state /floatlab/system/updates/host.json)\" = " + hostShellQuote(state) + "; curl -fsS http://127.0.0.1:8080/api/v1/health/ready; systemctl is-active --quiet floatlab-core-stack.service; flock -n /floatlab/system/updates/update.lock true"
		out, err := guestCommand(root, script)
		if strings.HasPrefix(out, "HOST_ACTIVATION_FAILED\n") {
			t.Fatalf("activation helper failed before reboot:\n%s", strings.TrimPrefix(out, "HOST_ACTIVATION_FAILED\n"))
		}
		if err != nil {
			return fmt.Errorf("waiting for %s boot: %s %w", state, out, err)
		}
		return nil
	})
}

func hostDownloadCandidate(t *testing.T, root, uri, directory string) {
	t.Helper()
	raw, err := command(time.Minute, root, nil, "virsh", "-c", uri, "net-dumpxml", "default")
	if err != nil {
		t.Fatal(err)
	}
	var network struct {
		IPs []struct {
			Address string `xml:"address,attr"`
			Family  string `xml:"family,attr"`
		} `xml:"ip"`
	}
	if err = xml.Unmarshal([]byte(raw), &network); err != nil {
		t.Fatal(err)
	}
	gateway := ""
	for _, ip := range network.IPs {
		if net.ParseIP(ip.Address).To4() != nil {
			gateway = ip.Address
		}
	}
	if gateway == "" {
		t.Fatal("default libvirt IPv4 gateway unavailable")
	}
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
	if out, e := guestCommand(root, script); e != nil {
		t.Fatal(out, e)
	}
	waitFor(t, 10*time.Minute, func() error {
		out, e := guestCommand(root, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
		if e != nil {
			return e
		}
		if strings.TrimSpace(out) == "running" {
			return fmt.Errorf("candidate download running")
		}
		if strings.TrimSpace(out) != "0" {
			logs, _ := guestCommand(root, "cat "+prefix+".log")
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
	if out, e := guestCommand(root, "echo "+hostShellQuote(manifest.Files["floatlab-root.tar.gz"]+"  /floatlab/system/updates/appliance-fixture/floatlab-root.tar.gz")+" | sha256sum --check"); e != nil {
		t.Fatalf("candidate transfer integrity: %s %v", out, e)
	}
}
