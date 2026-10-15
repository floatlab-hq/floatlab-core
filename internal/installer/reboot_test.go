package installer

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestRebootDoesNotExportMountedZFSRoot(t *testing.T) {
	for _, fs := range []string{"zfs", "tmpfs"} {
		t.Run(fs, func(t *testing.T) {
			root := t.TempDir()
			log := root + "/commands"
			t.Setenv("REBOOT_LOG", log)
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			for name, output := range map[string]string{"findmnt": fs, "zpool": "floatlab", "systemctl": ""} {
				script := "#!/bin/sh\necho '" + name + " '$* >> \"$REBOOT_LOG\"\necho '" + output + "'\n"
				if err := os.WriteFile(root+"/"+name, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := reboot(context.Background()); err != nil {
				t.Fatal(err)
			}
			commands, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(commands), "zpool export") != (fs == "tmpfs") {
				t.Fatalf("unsafe reboot:\n%s", commands)
			}
			if !strings.Contains(string(commands), "systemctl reboot") {
				t.Fatal("reboot was not requested")
			}
			if fs == "tmpfs" {
				stop := strings.Index(string(commands), "systemctl stop floatlab-installer-root.service")
				export := strings.Index(string(commands), "zpool export")
				if stop < 0 || stop > export || strings.Contains(string(commands), "docker.socket") {
					t.Fatalf("runtime was not stopped before export:\n%s", commands)
				}
			}
		})
	}
}
