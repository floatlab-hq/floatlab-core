package shellchecks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func write(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
}

func check(t *testing.T, condition bool, message string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(message, args...)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func run(t *testing.T, want int, command string, args ...string) string {
	t.Helper()
	out, err := exec.Command(command, args...).CombinedOutput()
	code := 0
	if failure, ok := err.(*exec.ExitError); ok {
		code = failure.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	check(t, code == want, "%s %v exited %d, want %d: %s", command, args, code, want, out)
	return string(out)
}

func module(name string) string {
	dir := os.Getenv("FLOATLAB_TEST_MODULES")
	if dir == "" {
		dir = "../../nixos/modules"
	}
	return filepath.Join(dir, name+".sh")
}

func shim(t *testing.T, directory string, names ...string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if err := os.Symlink(executable, filepath.Join(directory, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestInstallerAnswers(t *testing.T) {
	root := t.TempDir()
	media, runtime, commands := filepath.Join(root, "iso"), filepath.Join(root, "runtime"), filepath.Join(root, "bin")
	for _, dir := range []string{media, runtime, commands} {
		mkdir(t, dir)
	}
	t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_ROOT", root)
	t.Setenv("TEST_PARENT", "")
	for name, body := range map[string]string{
		"findmnt": `case "$*" in *SOURCE*) echo /dev/testusb1;; *) exit 1;; esac`,
		"lsblk":   `case "$*" in *PKNAME*) echo "$TEST_PARENT";; *TYPE*) if [ -n "$TEST_PARENT" ]; then printf "/dev/testusb1 part\n/dev/testusb2 part\n"; else echo rom; fi;; esac`,
		"mount":   `test "$2" = ro,nodev,nosuid,noexec || exit 1; cp "$TEST_ROOT/partition/"* "$4/"`,
		"umount":  `echo unmounted >> "$TEST_ROOT/unmounts"`,
	} {
		shell, err := exec.LookPath("sh")
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(commands, name), "#!"+shell+"\n"+body+"\n", 0700)
	}
	discover := func(want int, device string) {
		out := run(t, want, "bash", module("installer-answers"), media, runtime, device)
		check(t, !strings.Contains(out, "DO-NOT-LOG"), "logged answers: %q", out)
	}
	none := filepath.Join(root, "no-device")
	discover(1, none)
	answers := "# DO-NOT-LOG\nversion=1\n"
	first, second := filepath.Join(media, "floatlab-node.toml"), filepath.Join(media, "floatlab-other.toml")
	copied := filepath.Join(runtime, "answers.toml")
	write(t, first, answers, 0600)
	discover(0, none)
	check(t, read(t, copied) == answers, "copied answers differ")
	info, err := os.Stat(copied)
	if err != nil {
		t.Fatal(err)
	}
	check(t, info.Mode().Perm() == 0600, "answers permissions are %v", info.Mode().Perm())
	write(t, second, "version=1\n", 0600)
	discover(2, none)
	check(t, !exists(copied), "ambiguous media retained stale answers")
	for _, file := range []string{first, second} {
		if err := os.Remove(file); err != nil {
			t.Fatal(err)
		}
	}
	discover(1, none)
	check(t, !exists(copied), "missing media retained stale answers")
	partition := filepath.Join(root, "partition")
	mkdir(t, partition)
	write(t, filepath.Join(partition, "floatlab-usb.toml"), answers, 0600)
	t.Setenv("TEST_PARENT", "testusb")
	discover(0, none)
	check(t, read(t, copied) == answers && read(t, filepath.Join(root, "unmounts")) == "unmounted\n", "failed sibling-partition discovery")
	t.Setenv("TEST_PARENT", "")
	device := filepath.Join(root, "answers-cd")
	write(t, device, "", 0600)
	discover(0, device)
	check(t, read(t, copied) == answers && read(t, filepath.Join(root, "unmounts")) == "unmounted\nunmounted\n", "failed explicit answers device discovery")
}

func TestZFSMemory(t *testing.T) {
	const gib = 1 << 30
	for _, tc := range []struct{ memory, defaultMax, explicit, expected uint64 }{
		{3584 << 20, 3 * gib, 0, 512 << 20}, {4 * gib, 3 * gib, 0, gib}, {8 * gib, 6 * gib, 0, 5 * gib},
		{16 * gib, 12 * gib, 0, 0}, {4 * gib, 3 * gib, 256 << 20, 256 << 20}, {4 * gib, 3 * gib, 2 * gib, 2 * gib},
	} {
		t.Run(fmt.Sprint(tc.memory, "-", tc.explicit), func(t *testing.T) {
			dir := t.TempDir()
			for name, text := range map[string]string{
				"meminfo":  fmt.Sprintf("MemTotal: %d kB\n", tc.memory/1024),
				"arcstats": fmt.Sprintf("c_max 4 %d\n", tc.defaultMax),
				"arc_max":  fmt.Sprintf("%d\n", tc.explicit),
			} {
				write(t, filepath.Join(dir, name), text, 0600)
			}
			t.Setenv("FLOATLAB_MEMINFO_FILE", filepath.Join(dir, "meminfo"))
			t.Setenv("FLOATLAB_ARC_STATS_FILE", filepath.Join(dir, "arcstats"))
			t.Setenv("FLOATLAB_ARC_MAX_FILE", filepath.Join(dir, "arc_max"))
			run(t, 0, "bash", module("zfs-memory"))
			got := strings.TrimSpace(read(t, filepath.Join(dir, "arc_max")))
			check(t, got == fmt.Sprint(tc.expected), "ARC max = %s, want %d", got, tc.expected)
		})
	}
}

type storageState struct {
	Commands       [][]string
	Datasets       map[string]map[string]string
	Fail           []string
	RuntimeMounted bool
}

func fakeStorage(path string, args []string) int {
	var state storageState
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		panic(err)
	}
	state.Commands = append(state.Commands, args)
	defer func() {
		data, err := json.Marshal(state)
		if err != nil {
			panic(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			panic(err)
		}
	}()
	if len(state.Fail) > 0 && hasPrefix(args, state.Fail) {
		return 1
	}
	const installer = "floatlab/os/installer"
	last := args[len(args)-1]
	switch args[0] {
	case "zfs":
		switch args[1] {
		case "get":
			value, ok := state.Datasets[last][args[len(args)-2]]
			if !ok {
				return 1
			}
			fmt.Println(value)
		case "list":
			if state.Datasets[last] == nil {
				return 1
			}
		case "clone":
			state.Datasets[last] = map[string]string{"origin": args[len(args)-2], "mounted": "no",
				"readonly": "off", "org.floatlab:installer-runtime": os.Getenv("INVOCATION_ID")}
		case "destroy":
			delete(state.Datasets, last)
		default:
			return 1
		}
	case "mount":
		if args[1] == "-t" {
			state.Datasets[installer]["mounted"] = "yes"
		}
		if args[1] == "--bind" {
			state.RuntimeMounted = true
		}
	case "mountpoint":
		if last == "/run/floatlab-installer" && state.RuntimeMounted || last != "/run/floatlab-installer" && state.Datasets[installer]["mounted"] == "yes" {
			return 0
		}
		return 1
	case "umount":
		if last == "/run/floatlab-installer" {
			state.RuntimeMounted = false
		} else {
			state.Datasets[installer]["mounted"] = "no"
		}
	case "install":
		if args[1] == "-d" && strings.HasPrefix(last, filepath.Dir(path)+"/") {
			if err := os.MkdirAll(last, 0700); err != nil {
				panic(err)
			}
		}
	}
	return 0
}

func storageFixture(t *testing.T, name string, state *storageState, args ...string) {
	t.Helper()
	path := os.Getenv("FLOATLAB_STORAGE_FIXTURE")
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(data), 0600)
	shell := "bash"
	if name == "os-live" {
		shell = "sh"
	}
	command := exec.Command(shell, append([]string{"-c", strings.ReplaceAll(read(t, module(name)), "sshdir=/floatlab/system/etc/ssh",
		"sshdir="+filepath.Join(filepath.Dir(path), "ssh")), name}, args[1:]...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	check(t, (err == nil) == (args[0] == "ok"), "%s unexpected result %v: %s", name, err, output)
	*state = storageState{}
	if err := json.Unmarshal([]byte(read(t, path)), state); err != nil {
		t.Fatal(err)
	}
}

func commandIndex(commands [][]string, prefix ...string) int {
	return slices.IndexFunc(commands, func(command []string) bool { return hasPrefix(command, prefix) })
}

func TestOSLive(t *testing.T) {
	root := t.TempDir()
	shim(t, root, "zfs")
	t.Setenv("FLOATLAB_STORAGE_FIXTURE", filepath.Join(root, "state.json"))
	cmdline := filepath.Join(root, "cmdline")
	t.Setenv("FLOATLAB_CMDLINE_FILE", cmdline)
	const current, live, etc = "floatlab/os/current", "floatlab/os/live", "floatlab/system/etc"
	boot := func(t *testing.T, state *storageState, cmdlineText, result string) {
		write(t, cmdline, cmdlineText, 0600)
		storageFixture(t, "os-live", state, result)
	}
	fixture := func() storageState {
		return storageState{Datasets: map[string]map[string]string{
			current:          {"origin": "floatlab/os/v1@base", "readonly": "on", "org.floatlab:host-id": "12345678", "org.floatlab:version": "v1", "org.floatlab:init": "/nix/store/example/init"},
			"floatlab/os/v1": {"readonly": "on", "org.floatlab:host-id": "12345678"}, "floatlab/os/v1@base": {},
			live: {"mounted": "no", "writes": "dirty"}, etc: {"writes": "persistent"},
		}}
	}
	const line = "root=zfs:floatlab/os/current floatlab.installed=1 spl.spl_hostid=0x12345678 init=/nix/store/example/init"
	state := fixture()
	for range 2 {
		boot(t, &state, line, "ok")
		check(t, state.Datasets[live]["origin"] == "floatlab/os/v1@base" && state.Datasets[live]["writes"] == "" &&
			len(state.Datasets["floatlab/os/v1@base"]) == 0 && state.Datasets[etc]["writes"] == "persistent", "unexpected datasets: %+v", state.Datasets)
		state.Datasets[live]["writes"] = "new dirty write"
	}
	for _, scenario := range []struct{ dataset, key, value string }{
		{current, "origin", "floatlab/system/etc@base"}, {current, "origin", "floatlab/os/current@base"}, {live, "mounted", "yes"},
		{current, "org.floatlab:host-id", "87654321"}, {current, "org.floatlab:init", "/nix/store/other/init"}, {current, "org.floatlab:version", "v99"},
	} {
		state := fixture()
		state.Datasets[scenario.dataset][scenario.key] = scenario.value
		boot(t, &state, line, "fail")
		check(t, commandIndex(state.Commands, "zfs", "destroy") == -1, "destroyed live after invalid state: %v", scenario)
	}
	for _, invalid := range []string{"", "root=zfs:floatlab/system/etc", "root=zfs:floatlab/os/current root=zfs:floatlab/os/next"} {
		state := fixture()
		boot(t, &state, invalid, "fail")
		check(t, commandIndex(state.Commands, "zfs", "destroy") == -1, "destroyed live with invalid cmdline %q", invalid)
	}
	for _, failure := range []string{"destroy", "clone"} {
		state := fixture()
		state.Fail = []string{"zfs", failure}
		boot(t, &state, line, "fail")
		check(t, state.Datasets[current] != nil && state.Datasets["floatlab/os/v1@base"] != nil, "modified pristine datasets")
	}
	state = fixture()
	state.Datasets["floatlab/os/next"] = map[string]string{"origin": "floatlab/os/v2@base", "readonly": "on", "org.floatlab:host-id": "12345678", "org.floatlab:version": "v2", "org.floatlab:init": "/nix/store/example/init"}
	state.Datasets["floatlab/os/v2"] = map[string]string{"readonly": "on", "org.floatlab:host-id": "12345678"}
	state.Datasets["floatlab/os/v2@base"] = map[string]string{}
	boot(t, &state, "root=zfs:floatlab/os/next floatlab.trial=operation spl.spl_hostid=0x12345678 init=/nix/store/example/init", "ok")
	check(t, state.Datasets[live]["origin"] == "floatlab/os/v2@base", "trial ignored candidate origin")
}

func TestInstallerRoot(t *testing.T) {
	root := t.TempDir()
	shim(t, root, "zfs", "mount", "mountpoint", "umount", "chroot", "mkdir", "rm", "cp", "ln", "touch", "install", "mv", "ssh-keygen", "systemctl")
	t.Setenv("FLOATLAB_STORAGE_FIXTURE", filepath.Join(root, "state.json"))
	t.Setenv("INVOCATION_ID", "test-invocation")
	const installer, current = "floatlab/os/installer", "floatlab/os/current"
	fixture := func() storageState {
		return storageState{Datasets: map[string]map[string]string{
			current: {"origin": "floatlab/os/v1@base"}, "floatlab/os/v1": {"readonly": "on"},
		}}
	}
	invoke := func(state *storageState, result, action string) {
		t.Helper()
		storageFixture(t, "installer-root", state, result, action)
	}

	state := fixture()
	invoke(&state, "ok", "prepare")
	private := commandIndex(state.Commands, "mount", "--make-private", "/run/floatlab-installer")
	clone := commandIndex(state.Commands, "zfs", "clone")
	check(t, state.Datasets[installer]["origin"] == "floatlab/os/v1@base" && private >= 0 && clone > private &&
		commandIndex(state.Commands, "mount", "--make-rslave", "/run/floatlab-installer/root/floatlab") >= 0, "invalid clone setup: %+v", state)
	for _, kind := range []string{"rsa", "ed25519"} {
		name := "ssh_host_" + kind + "_key"
		key := filepath.Join(root, "ssh", name)
		check(t, commandIndex(state.Commands, "install", "-m", "0600", "/etc/ssh/"+name, key+".tmp") >= 0 &&
			commandIndex(state.Commands, "ssh-keygen", "-y", "-f", key) >= 0, "SSH identity not adopted: %s", name)
	}
	reload, chroot := commandIndex(state.Commands, "systemctl", "reload-or-restart", "sshd.service"), commandIndex(state.Commands, "chroot")
	check(t, reload >= 0 && reload < chroot, "SSH service wasn't reloaded before chroot")
	invoke(&state, "ok", "cleanup")
	check(t, state.Datasets[installer] == nil && !state.RuntimeMounted &&
		reflect.DeepEqual(state.Datasets["floatlab/os/v1"], map[string]string{"readonly": "on"}), "cleanup changed pristine root: %+v", state)
	invoke(&state, "ok", "cleanup")
	mkdir(t, filepath.Join(root, "ssh"))
	for _, kind := range []string{"rsa", "ed25519"} {
		write(t, filepath.Join(root, "ssh", "ssh_host_"+kind+"_key"), "existing private key", 0600)
	}
	state.Commands = nil
	invoke(&state, "ok", "prepare")
	check(t, commandIndex(state.Commands, "install", "-m", "0600") == -1, "overwrote existing SSH identity")

	state = fixture()
	state.Datasets[current]["origin"] = "floatlab/system/etc@base"
	invoke(&state, "fail", "prepare")
	check(t, commandIndex(state.Commands, "zfs", "clone") == -1, "cloned an invalid origin")
	state = fixture()
	invoke(&state, "ok", "prepare")
	invoke(&state, "fail", "prepare")
	t.Setenv("INVOCATION_ID", "another-invocation")
	invoke(&state, "fail", "cleanup")
	check(t, commandIndex(state.Commands, "zfs", "destroy") == -1, "destroyed an existing or foreign clone")
	t.Setenv("INVOCATION_ID", "test-invocation")

	state = fixture()
	invoke(&state, "ok", "prepare")
	state.Fail = []string{"umount", "--recursive"}
	invoke(&state, "fail", "cleanup")
	check(t, state.Datasets[installer] != nil, "destroyed after failed unmount")
	state.Fail = nil
	state.Datasets[installer]["org.floatlab:installer-runtime"] = "off"
	invoke(&state, "fail", "cleanup")
	check(t, commandIndex(state.Commands, "zfs", "destroy") == -1, "destroyed an unowned clone")

	state = fixture()
	state.Fail = []string{"mount", "--rbind"}
	invoke(&state, "fail", "prepare")
	invoke(&state, "ok", "cleanup")
	check(t, state.Datasets[installer] == nil, "retained clone after partial preparation")

	state = fixture()
	invoke(&state, "ok", "prepare")
	state.Fail = []string{"zfs", "destroy"}
	invoke(&state, "fail", "cleanup")
	var destroys [][]string
	for _, command := range state.Commands {
		if hasPrefix(command, []string{"zfs", "destroy"}) {
			destroys = append(destroys, command)
		}
	}
	check(t, reflect.DeepEqual(destroys, [][]string{{"zfs", "destroy", installer}}) && state.RuntimeMounted, "cleanup removed runtime after failed destroy")
}

func TestMain(m *testing.M) {
	name := filepath.Base(os.Args[0])
	if path := os.Getenv("FLOATLAB_STORAGE_FIXTURE"); path != "" && !strings.HasSuffix(name, ".test") {
		os.Exit(fakeStorage(path, append([]string{name}, os.Args[1:]...)))
	}
	if path := os.Getenv("FLOATLAB_VM_FIXTURE"); path != "" {
		os.Exit(fakeLauncher(path, append([]string{name}, os.Args[1:]...)))
	}
	os.Exit(m.Run())
}

func hasPrefix(values, prefix []string) bool {
	return len(values) >= len(prefix) && slices.Equal(values[:len(prefix)], prefix)
}
