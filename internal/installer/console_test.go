package installer

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/credentials"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestConsoleSettingsAndCancelledCountdown(t *testing.T) {
	var output bytes.Buffer
	console := Console{In: bufio.NewReader(strings.NewReader("UTC\nbad name\ndhcp\nUTC\nnode-a\nstatic\n192.0.2.20\n255.255.255.0\n192.0.2.1\n1.1.1.1,8.8.8.8\n")), Out: &output, FD: -1}
	a, err := console.settings(Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)})
	if err != nil || a.NodeName != "node-a" || a.Network.IPv4.Address != "192.0.2.20" {
		t.Fatal(a, err)
	}
	if !strings.Contains(output.String(), "Invalid settings") {
		t.Fatal("invalid settings not reported")
	}
	console.In = bufio.NewReader(strings.NewReader("\n"))
	err = console.countdown(context.Background())
	if !errors.Is(err, io.EOF) || !strings.Contains(output.String(), "Automatic reboot cancelled") {
		t.Fatal("cancelled countdown did not stop reboot", err)
	}
}

func TestConsoleReattachesToActiveDiskWorker(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("downloadFailure=", failure), func(t *testing.T) {
			root := t.TempDir()
			runtimeDir := root + "/run"
			persistentDir := root + "/system/etc"
			bin := root + "/bin"
			os.MkdirAll(bin, 0700)
			t.Setenv("TEST_RUN", runtimeDir)
			t.Setenv("TEST_LOG", root+"/commands")
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			result := Status{Started: time.Now(), Phase: "Installation complete", PoolReady: true, StorageReady: true, Ready: true, Address: "http://192.0.2.10:8080"}
			if failure {
				result.Ready = false
				result.Phase = "Fetch images"
				result.Error = "image download failed"
			}
			if err := credentials.WritePrivate(root+"/result.json", result); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_RESULT", root+"/result.json")
			systemctl := `#!/bin/sh
 echo "$*" >> "$TEST_LOG"
 if [ "$1" = show ]; then
   if [ -e "$TEST_RUN/submission.json" ]; then cp "$TEST_RESULT" "$TEST_RUN/status.json"; chmod 600 "$TEST_RUN/status.json"; fi
   echo active
 fi
`
			if err := os.WriteFile(bin+"/systemctl", []byte(systemctl), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bin+"/timedatectl", []byte("#!/bin/sh\necho no\n"), 0700); err != nil {
				t.Fatal(err)
			}
			identity := Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)}
			if err := credentials.WritePrivate(runtimeDir+"/operation.json", Operation{Identity: identity}); err != nil {
				t.Fatal(err)
			}
			if err := credentials.WritePrivate(runtimeDir+"/status.json", Status{Phase: "Copy image and install EFI bootloaders", Started: time.Now()}); err != nil {
				t.Fatal(err)
			}
			if err := credentials.WriteBytes(runtimeDir+"/credential.key", make([]byte, 32)); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			console := Console{In: bufio.NewReader(strings.NewReader("\nUTC\nnode-a\ndhcp\n")), Out: &output, FD: -1}
			if err := console.run(context.Background(), runtimeDir, persistentDir); !errors.Is(err, io.EOF) {
				t.Fatal("disconnect did not preserve staged host settings", err)
			}
			console.In = bufio.NewReader(strings.NewReader("\n\n\n\nadmin\npassword\npassword\n\n"))
			err := console.run(context.Background(), runtimeDir, persistentDir)
			if failure {
				if err == nil || !strings.Contains(output.String(), "image download failed") {
					t.Fatal(err, output.String())
				}
			} else if !errors.Is(err, io.EOF) || !strings.Contains(output.String(), "Automatic reboot cancelled") {
				t.Fatal(err, output.String())
			}
			if !strings.Contains(output.String(), "TOTP enrollment is unavailable") {
				t.Fatal("NTP absence did not skip enrollment")
			}
			var submission Submission
			if e := credentials.ReadPrivate(runtimeDir+"/submission.json", &submission); e != nil {
				t.Fatal(e)
			}
			if submission.Host.NodeName != "node-a" || submission.Admin.Username != "admin" || submission.VerificationPassword == "password" {
				t.Fatal("reattached console did not stage credentials safely")
			}
			log, _ := os.ReadFile(root + "/commands")
			if strings.Contains(string(log), "start") || strings.Contains(string(log), "reboot") {
				t.Fatal("active worker restarted or failure rebooted", string(log))
			}
		})
	}
}

func TestStartWorkerDoesNotResetUnusedUnit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	t.Setenv("TEST_LOG", root+"/commands")
	script := `#!/bin/sh
 echo "$*" >> "$TEST_LOG"
 case "$1" in show) echo inactive;; reset-failed) exit 1;; esac
`
	if err := os.WriteFile(root+"/systemctl", []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := startWorker(context.Background()); err != nil {
		t.Fatal(err)
	}
	commands, _ := os.ReadFile(root + "/commands")
	if strings.Contains(string(commands), "reset-failed") || !strings.Contains(string(commands), "start --no-block") {
		t.Fatal(string(commands))
	}
}
