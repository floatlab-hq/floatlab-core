package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"golang.org/x/crypto/bcrypt"
)

func TestWorkerRetriesFinalizationWithoutErasing(t *testing.T) {
	root := t.TempDir()
	runtimeDir := root + "/run"
	persistentDir := root + "/system/etc"
	mediaDir := root + "/media"
	bin := root + "/bin"
	for _, dir := range []string{runtimeDir, persistentDir, mediaDir, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	log := root + "/commands"
	t.Setenv("INSTALL_TEST_LOG", log)
	for _, name := range []string{"wipefs", "sfdisk", "udevadm", "mkfs.vfat", "mkfs.ext4", "zpool", "mount", "mountpoint", "umount", "grub-install", "hostnamectl", "timedatectl", "blkid", "findmnt", "systemctl"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$INSTALL_TEST_LOG\"\n"
		if name == "findmnt" {
			script += "echo /dev/sr0\n"
		}
		if name == "blkid" {
			script += "echo test-uuid\n"
		}
		if name == "grub-install" {
			script += "if [ \"${INSTALL_FAIL_GRUB:-}\" = 1 ]; then exit 1; fi\n"
		}
		if name == "systemctl" {
			script += "case \"$*\" in *floatlab-core-stack.service*) exit 1;; esac\n"
		}
		script += "exit 0\n"
		if err := os.WriteFile(bin+"/"+name, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	p := Payload{Kernel: "kernel", Initrd: "initrd", Init: "/nix/store/example/init", Files: map[string]string{}}
	for _, name := range []string{"kernel", "initrd", "nix-store.squashfs"} {
		data := []byte("test " + name)
		if err := os.WriteFile(mediaDir+"/"+name, data, 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		p.Files[name] = hex.EncodeToString(sum[:])
		p.Bytes += int64(len(data))
	}
	if err := credentials.WritePrivate(mediaDir+"/floatlab-manifest.json", p); err != nil {
		t.Fatal(err)
	}
	disks := testDisks()[:1]
	a := Appliance{NodeID: "node", NodeName: "floatlab", Timezone: "UTC", HostID: "12345678", JWTSecret: strings.Repeat("a", 64), Network: hostnetwork.DefaultConfig()}
	op := Operation{Disks: disks, Command: ZPoolCommand(disks), Identity: a}
	if err := credentials.WritePrivate(runtimeDir+"/operation.json", op); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	if err := credentials.WriteBytes(runtimeDir+"/credential.key", key); err != nil {
		t.Fatal(err)
	}
	hash, _ := bcrypt.GenerateFromPassword([]byte("test"), bcrypt.MinCost)
	proof, _ := credentials.Encrypt(key, "test")
	sub := Submission{Host: a, Admin: credentials.Bootstrap{Username: "admin", PasswordHash: string(hash), NodeID: "node", NodeName: "floatlab"}, VerificationPassword: proof}
	if err := credentials.WritePrivate(runtimeDir+"/submission.json", sub); err != nil {
		t.Fatal(err)
	}
	inventory := func(context.Context) ([]Disk, error) { return disks, nil }
	t.Setenv("INSTALL_FAIL_GRUB", "1")
	for attempt := 0; attempt < 3; attempt++ {
		if err := worker(context.Background(), runtimeDir, persistentDir, mediaDir, inventory); err == nil {
			t.Fatal("failed stack reported success")
		}
		var status Status
		if err := credentials.ReadPrivate(runtimeDir+"/status.json", &status); err != nil {
			t.Fatal(err)
		}
		if !status.PoolReady || status.StorageReady != (attempt > 0) || status.Ready || status.Error == "" {
			t.Fatalf("unsafe checkpoint: %+v", status)
		}
		if _, err := os.Stat(persistentDir + "/installed"); !os.IsNotExist(err) {
			t.Fatal("completion marker written on failure")
		}
		t.Setenv("INSTALL_FAIL_GRUB", "")
		inventory = func(context.Context) ([]Disk, error) { t.Fatal("retry must not inspect/erase disks"); return nil, nil }
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"wipefs --all", "sfdisk --wipe", "zpool create", "mkfs.ext4 -F"} {
		if strings.Count(string(commands), command) != 1 {
			t.Fatalf("destructive work repeated: %s\n%s", command, commands)
		}
	}
	if strings.Count(string(commands), "grub-install --target") != 2 || strings.Count(string(commands), "umount "+runtimeDir+"/image-0") != 2 || strings.Count(string(commands), "umount "+runtimeDir+"/efi-0") != 2 {
		t.Fatal("image-copy retry leaked mounts", string(commands))
	}
	if strings.Contains(string(commands), "reboot") {
		t.Fatal("failed installation rebooted")
	}
	// A checkpoint before storage is ready may never replay destructive work.
	if err := credentials.WritePrivate(runtimeDir+"/status.json", Status{Started: time.Now(), Phase: "Partition and format disks"}); err != nil {
		t.Fatal(err)
	}
	if err := worker(context.Background(), runtimeDir, persistentDir, mediaDir, inventory); err == nil || !strings.Contains(err.Error(), "interrupted destructive") {
		t.Fatal(err)
	}
	written := filepath.Join(runtimeDir, "image-0", "kernel")
	if data, err := os.ReadFile(written); err != nil || string(data) != "test kernel" {
		t.Fatal("payload not copied", err)
	}
}
func TestCopyRejectsCorruptPayload(t *testing.T) {
	root := t.TempDir()
	source := root + "/source"
	os.WriteFile(source, []byte("corrupt"), 0600)
	if err := copyVerified(source, root+"/target", strings.Repeat("a", 64)); err == nil {
		t.Fatal("corrupt payload accepted")
	}
}
