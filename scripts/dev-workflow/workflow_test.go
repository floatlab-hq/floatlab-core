package devworkflow

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fixture struct {
	t                      *testing.T
	root, system, bin, log string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{t, root, filepath.Join(root, "system"), filepath.Join(root, "bin"), filepath.Join(root, "commands")}
	for _, dir := range []string{filepath.Join(f.system, "etc"), filepath.Join(root, "workspace"), f.bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LOG", f.log)
	return f
}

func (f fixture) fake(name, source string) {
	f.t.Helper()
	f.put(filepath.Join(f.bin, name), "#!/usr/bin/env bash\nset -eu\n"+source+"\n", 0755)
}

func (f fixture) put(path, text string, mode os.FileMode) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f fixture) read(path string) string {
	f.t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f fixture) require(ok bool, message string, args ...any) {
	f.t.Helper()
	if !ok {
		f.t.Fatalf(message, args...)
	}
}

func (f fixture) run(ok bool, path string, args ...string) string {
	f.t.Helper()
	source := strings.NewReplacer("/floatlab/system", f.system, "/workspace", filepath.Join(f.root, "workspace"),
		"/run/floatlab-mode.lock", filepath.Join(f.root, "mode.lock")).Replace(f.read(filepath.Join("../..", path)))
	out, err := exec.Command("bash", append([]string{"-c", source, path}, args...)...).CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			f.t.Fatal(err)
		}
	}
	f.require((err == nil) == ok, "%s %v: %v\n%s", path, args, err, out)
	return strings.TrimSpace(string(out))
}

func TestModePreservesCredentialsAndPreflightsImage(t *testing.T) {
	f := newFixture(t)
	f.fake("id", "echo 0")
	f.fake("docker", `echo "docker $*" >> "$LOG"; exit "${IMAGE_STATUS:-0}"`)
	f.fake("systemctl", `echo "systemctl $*" >> "$LOG"`)
	f.fake("floatlab-start-core-stack", `echo stack >> "$LOG"`)
	const mode = "scripts/floatlab-mode.sh"
	marker := filepath.Join(f.system, "etc/development")
	f.require(f.run(true, mode) == "production", "initial mode differs")
	t.Setenv("IMAGE_STATUS", "1")
	f.run(false, mode, "dev")
	_, err := os.Stat(marker)
	f.require(os.IsNotExist(err), "development activated without image: %v", err)
	t.Setenv("IMAGE_STATUS", "0")
	f.run(true, mode, "dev")
	f.require(f.run(true, mode) == "dev", "development did not activate")
	devHome := filepath.Join(f.system, "dev/home")
	info, err := os.Stat(devHome)
	if err != nil {
		t.Fatal(err)
	}
	f.require(info.Mode().Perm() == 0700, "development home permissions are %v", info.Mode().Perm())
	credential := filepath.Join(devHome, "credential")
	f.put(credential, "persist me", 0600)
	f.run(true, mode, "production")
	_, err = os.Stat(marker)
	f.require(os.IsNotExist(err) && f.read(credential) == "persist me", "production transition lost credentials or marker: %v", err)
	f.run(false, mode, "invalid")
	f.require(strings.Count(f.read(f.log), "systemctl restart floatlab-hostd") == 2, "unexpected service restarts: %s", f.read(f.log))
}

func TestHostdOnlyReplacesSuccessfulBuild(t *testing.T) {
	f := newFixture(t)
	f.fake("go", `if [ "${FAIL_BUILD:-0}" = 1 ]; then exit 1; fi; printf new > "$3"`)
	const deploy = "docker/dev/hostd.sh"
	f.run(false, deploy)
	f.put(filepath.Join(f.system, "etc/development"), "", 0600)
	binary := filepath.Join(f.system, "dev/bin/floatlab-hostd")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	f.put(binary, "old", 0600)
	original, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAIL_BUILD", "1")
	f.run(false, deploy)
	f.require(f.read(binary) == "old", "failed build replaced installed executable")
	t.Setenv("FAIL_BUILD", "0")
	f.run(true, deploy)
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	f.require(f.read(binary) == "new" && !os.SameFile(original, info) && info.Mode().Perm() == 0755, "deployment did not atomically replace binary")
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(binary), ".hostd.*"))
	if err != nil {
		t.Fatal(err)
	}
	f.require(len(matches) == 0, "temporary builds remain: %v", matches)
}

func TestPRPushesReviewedCommits(t *testing.T) {
	f := newFixture(t)
	f.fake("git", `case "$*" in
		'status --porcelain') printf '%s' "${DIRTY:-}";;
		'symbolic-ref --short HEAD') echo "${BRANCH:-codex/test}";;
		*) echo "git $*" >> "$LOG";;
	esac`)
	f.fake("gh", `echo "gh $*" >> "$LOG"`)
	const script = "docker/dev/pr.sh"
	t.Setenv("DIRTY", "")
	t.Setenv("BRANCH", "codex/test")
	f.run(false, script)
	t.Setenv("DIRTY", "modified")
	f.run(false, script, "Title")
	t.Setenv("DIRTY", "")
	t.Setenv("BRANCH", "main")
	f.run(false, script, "Title")
	t.Setenv("BRANCH", "codex/test")
	f.run(true, script, "Title")
	commands := f.read(f.log)
	f.require(strings.Count(commands, "git push") == 1 && strings.Contains(commands, "git push --set-upstream origin codex/test") &&
		strings.Contains(commands, "gh pr create --draft --fill --title Title"), "unexpected PR commands: %s", commands)
}

func TestPROffersDeviceLogin(t *testing.T) {
	f := newFixture(t)
	f.fake("git", `case "$*" in
		'status --porcelain') ;;
		'symbolic-ref --short HEAD') echo codex/test;;
	esac`)
	f.fake("gh", `echo "gh $*" >> "$LOG"; if [ "$*" = 'auth status' ]; then exit 1; fi`)
	f.run(true, "docker/dev/pr.sh", "Title")
	f.require(strings.Contains(f.read(f.log), "gh auth login --hostname github.com --git-protocol https --web"), "device login not offered")
}

func TestComposeKeepsInstalledIdentityAndData(t *testing.T) {
	dir := "../../floatlab-appliance-image/files/core-stack"
	out, err := exec.Command("docker", "compose", "-f", filepath.Join(dir, "docker-compose.yml"), "-f",
		filepath.Join(dir, "docker-compose.dev.yml"), "config", "--format", "json").Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Fatalf("Compose config: %v\n%s", err, failure.Stderr)
		}
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Image       string
			Environment map[string]string
			Volumes     []struct{ Source, Target string }
			Ports       []struct {
				Target int
				HostIP string `json:"host_ip"`
			}
		}
	}
	if err := json.Unmarshal(out, &compose); err != nil {
		t.Fatal(err)
	}
	control := compose.Services["floatlab-control"]
	_, applianceConfig := control.Environment["FLOATLAB_APPLIANCE_CONFIG"]
	_, bootstrapPassword := control.Environment["FLOATLAB_BOOTSTRAP_PASSWORD"]
	if control.Image != "floatlab-dev:local" || control.Environment["FLOATLAB_DEV_MODE"] != "true" || !applianceConfig || bootstrapPassword {
		t.Fatalf("incorrect image or environment: %+v", control)
	}
	mounts := make(map[string]string)
	for _, mount := range control.Volumes {
		mounts[mount.Target] = mount.Source
	}
	if mounts["/var/lib/floatlab/raft"] != "/floatlab/system/management/raft" || mounts["/root"] != "/floatlab/system/dev/home" {
		t.Fatalf("unexpected mounts: %v", mounts)
	}
	for _, port := range control.Ports {
		if port.Target == 9090 && port.HostIP == "127.0.0.1" {
			return
		}
	}
	t.Fatal("developer editor is not bound to loopback")
}
