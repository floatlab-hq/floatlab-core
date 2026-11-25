package shellchecks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeLauncher(path string, args []string) int {
	log, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(log).Encode(args); err != nil {
		panic(err)
	}
	if err := log.Close(); err != nil {
		panic(err)
	}
	switch args[0] {
	case "virsh":
		if args[3] == "dominfo" || args[3] == "vol-info" {
			if os.Getenv("FLOATLAB_HARNESS_EXISTING") != "1" {
				return 1
			}
		}
	case "xorriso":
		input, err := os.ReadFile(filepath.Join(args[len(args)-1], "floatlab-answers.toml"))
		if err != nil {
			panic(err)
		}
		expected, err := os.ReadFile(os.Getenv("FLOATLAB_ANSWERS"))
		if err != nil {
			panic(err)
		}
		output := args[slices.Index(args, "-o")+1]
		if err := os.WriteFile(output, []byte("private-mock-ISO"), 0666); err != nil {
			panic(err)
		}
		info, err := os.Stat(output)
		if err != nil {
			panic(err)
		}
		if string(input) != string(expected) || info.Mode().Perm() != 0600 {
			return 1
		}
	case "qemu-img":
		if err := os.WriteFile(args[len(args)-2], nil, 0600); err != nil {
			panic(err)
		}
	}
	return 0
}

func TestVMLaunchers(t *testing.T) {
	root := t.TempDir()
	bin, firmware := filepath.Join(root, "bin"), filepath.Join(root, "firmware")
	mkdir(t, bin)
	mkdir(t, firmware)
	for _, name := range []string{"OVMF_CODE.fd", "OVMF_VARS.fd"} {
		write(t, filepath.Join(firmware, name), "", 0600)
	}
	shim(t, bin, "virsh", "virt-install", "xorriso", "qemu-system-x86_64", "qemu-img")
	log := filepath.Join(root, "commands.jsonl")
	iso, answers := filepath.Join(root, "release.iso"), filepath.Join(root, "answers.toml")
	write(t, iso, "unchanged-release-ISO", 0600)
	write(t, answers, "version = 1\n# hash-only answers fixture\n", 0600)
	for k, v := range map[string]string{
		"FLOATLAB_VM_FIXTURE": log, "VM_NAME": "owned-test-host", "LIBVIRT_POOL": "owned-test-pool", "LIBVIRT_URI": "test:///default",
		"FLOATLAB_ANSWERS": answers, "FLOATLAB_APPLIANCE_ISO": iso, "FLOATLAB_OVMF": firmware, "FLOATLAB_TEST_DISK": filepath.Join(root, "blank.raw"),
		"FLOATLAB_HARNESS_EXISTING": "",
	} {
		t.Setenv(k, v)
	}
	launch := func(script string, want int) [][]string {
		t.Helper()
		write(t, log, "", 0600)
		run(t, want, "bash", script, iso)
		var commands [][]string
		for line := range strings.SplitSeq(strings.TrimSpace(read(t, log)), "\n") {
			var command []string
			if err := json.Unmarshal([]byte(line), &command); err != nil {
				t.Fatal(err)
			}
			commands = append(commands, command)
			if command[0] == "xorriso" {
				check(t, slices.Contains(command, "FLOATLAB_ANSWERS") && slices.Contains(command, "-R"), "wrong answers media options: %v", command)
				check(t, !exists(command[len(command)-1]) && !exists(command[slices.Index(command, "-o")+1]), "private answers media leaked")
			}
		}
		return commands
	}
	const libvirt = "../../scripts/run-libvirt.sh"
	for _, script := range []string{libvirt, "../../../scripts/create-floatlab-vm.sh"} {
		commands := launch(script, 0)
		index := commandIndex(commands, "virt-install")
		check(t, index >= 0, "installer was not started")
		args := commands[index]
		boot := slices.Index(args, "--boot")
		check(t, boot >= 0 && strings.HasPrefix(args[boot+1], "uefi") && !slices.Contains(args, "--transient"), "wrong libvirt boot options: %v", args)
		for _, part := range []string{"serial=floatlab-zfs,boot_order=1", "device=cdrom,readonly=on,boot_order=2", "owned-test-host-answers.iso,device=cdrom,readonly=on"} {
			check(t, slices.ContainsFunc(args, func(arg string) bool { return strings.Contains(arg, part) }), "missing %s", part)
		}
		check(t, slices.ContainsFunc(commands, func(c []string) bool { return c[0] == "virsh" && slices.Contains(c, "vol-upload") }), "answers volume not uploaded")
	}
	t.Setenv("FLOATLAB_ANSWERS", "")
	check(t, commandIndex(launch(libvirt, 0), "xorriso") == -1, "interactive install unexpectedly used answers")
	t.Setenv("FLOATLAB_HARNESS_EXISTING", "1")
	commands := launch(libvirt, 1)
	check(t, !slices.ContainsFunc(commands, func(c []string) bool {
		return c[0] == "virt-install" || c[0] == "xorriso" || slices.Contains(c, "vol-create-as")
	}), "existing VM was modified")
	t.Setenv("FLOATLAB_HARNESS_EXISTING", "")
	t.Setenv("FLOATLAB_ANSWERS", answers)
	commands = launch("../../scripts/run-qemu.sh", 0)
	index := commandIndex(commands, "qemu-system-x86_64")
	check(t, index >= 0, "QEMU was not started")
	args := strings.Join(commands[index], " ")
	check(t, strings.Contains(args, "pflash") && strings.Contains(args, "OVMF_CODE.fd") && strings.Contains(args, "serial=floatlab-zfs"), "invalid QEMU boot: %s", args)
	check(t, read(t, iso) == "unchanged-release-ISO", "release ISO was changed")
}
