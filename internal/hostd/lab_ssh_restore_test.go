package hostd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLabSSHWrappersRouteRestoreWithoutEvaluatingCommands(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"sudo", "floatlab-hostd"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$SSH_ORIGINAL_COMMAND\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, script := range []string{labSSHWrapperScript, labSSHRootScript} {
		for _, command := range []string{"zfs-backup state floatlab/backups/source/data -", "zfs-restore receive floatlab/restores/source/op/data metadata"} {
			cmd := exec.Command("sh", "-c", script, "wrapper", "source", command)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SSH_ORIGINAL_COMMAND="+command)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != command {
				t.Fatalf("allowed command %q: %s %v", command, out, err)
			}
		}
		cmd := exec.Command("sh", "-c", script, "wrapper", "source", "touch /tmp/floatlab-should-never-exist")
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "SSH_ORIGINAL_COMMAND=touch /tmp/floatlab-should-never-exist")
		if err := cmd.Run(); err == nil {
			t.Fatal("SSH wrapper accepted arbitrary shell command")
		}
	}
}
