package managementupdate

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/internal/update"
)

func startupGuard(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../scripts/floatlab-core-start-guard.sh")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCoreStartupRetainsSharedTransactionLock(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("bash", "-ec", `source "$1"; floatlab_core_start_lock "$2"; echo locked; read -r finish`, "startup-test", startupGuard(t), dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "locked\n" {
		t.Fatalf("startup did not acquire lock: %q %v", line, err)
	}
	if unlock, err := update.Acquire(dir); err == nil {
		unlock()
		t.Fatal("update accepted while generic startup holds its lock")
	}
	_, _ = stdin.Write([]byte("finish\n"))
	_ = stdin.Close()
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	unlock, err := update.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	// Conversely, a live updater prevents generic startup from reaching Docker.
	cmd = exec.Command("bash", "-ec", `source "$1"; floatlab_core_start_lock "$2"; exit 99`, "startup-test", startupGuard(t), dir)
	if err = cmd.Run(); err == nil || cmd.ProcessState.ExitCode() == 99 {
		t.Fatal("startup continued while an update owned the lock")
	}
}

func TestCoreStartupRecognizesOnlyCompleteManagementProject(t *testing.T) {
	dir := t.TempDir()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	docker := "#!" + bash + `
[[ "$*" == *"label=com.docker.compose.project=floatlab-system"* ]] || exit 20
case "$MODE" in
  first-boot|foreign-project) exit 0 ;;
  missing-control) [[ "$*" == *"service=floatlab-control"* ]] && exit 0 ;;
  duplicate) printf 'one\ntwo\n'; exit 0 ;;
  failed-docker) exit 1 ;;
esac
echo one
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"recovered", "first-boot", "foreign-project", "missing-control", "duplicate", "failed-docker"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command("bash", "-ec", `source "$1"; floatlab_core_stack_running`, "startup-test", startupGuard(t))
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MODE="+mode)
			err := cmd.Run()
			if (err == nil) != (mode == "recovered") {
				t.Fatalf("complete recovered stack=%v, error=%v", mode == "recovered", err)
			}
		})
	}
	cmd := exec.Command("bash", "-ec", `source "$1"; floatlab_core_stack_running true`, "startup-test", startupGuard(t))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MODE=recovered")
	if err := cmd.Run(); err == nil {
		t.Fatal("mode switch reused containers instead of applying its Compose override")
	}
}

func TestModeSwitchPassesItsUpdateLockToLauncher(t *testing.T) {
	dir := t.TempDir()
	system := filepath.Join(dir, "system")
	if err := os.MkdirAll(filepath.Join(system, "etc"), 0700); err != nil {
		t.Fatal(err)
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!"+bash+"\n"+body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write("id", "echo 0\n")
	write("systemctl", "exit 0\n")
	write("docker", "exit 0\n")
	write("floatlab-start-core-stack", `set -e
test "$1" = --mode-switch
source "$GUARD"
floatlab_core_start_inherited_lock "$SYSTEM/updates"
if test -f "$SYSTEM/etc/development"; then echo dev; else echo production; fi
`)
	mode, err := os.ReadFile("../../scripts/floatlab-mode.sh")
	if err != nil {
		t.Fatal(err)
	}
	mode = []byte(strings.ReplaceAll(string(mode), "/floatlab/system", system))
	for _, action := range []string{"dev", "production"} {
		cmd := exec.Command(bash, "-s", "--", action)
		cmd.Stdin = strings.NewReader(string(mode))
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SYSTEM="+system, "GUARD="+startupGuard(t))
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), action+"\n") {
			t.Fatalf("mode switch failed its inherited-lock handoff: %s %v", out, err)
		}
	}
	// An independent invocation cannot claim an inherited mode-switch lock.
	cmd := exec.Command(bash, "-ec", `source "$1"; floatlab_core_start_inherited_lock "$2"`, "startup-test", startupGuard(t), filepath.Join(system, "updates"))
	if err = cmd.Run(); err == nil {
		t.Fatal("mode-switch accepted without inherited transaction lock")
	}
}
