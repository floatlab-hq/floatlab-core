package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
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
	tarBin, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	// The sandbox cannot chown to root; exercise real extraction without ownership changes.
	if err := os.WriteFile(bin+"/tar", []byte("#!/bin/sh\nexec "+tarBin+" \"$@\" --no-same-owner\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"wipefs", "sfdisk", "udevadm", "mkfs.vfat", "zpool", "mount", "mountpoint", "umount", "zbm-kcl", "hostnamectl", "timedatectl", "blkid", "findmnt", "systemctl", "nix-store", "sync"} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$INSTALL_TEST_LOG\"\n"
		if name == "findmnt" {
			script += "echo /dev/sr0\n"
		}
		if name == "blkid" {
			script += "echo test-uuid\n"
		}
		if name == "zbm-kcl" {
			script += "if [ \"${INSTALL_FAIL_BOOTLOADER:-}\" = 1 ]; then exit 1; fi\n"
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
	t.Setenv("INSTALL_ZFS_STATE", root+"/zfs-state")
	if err := os.Mkdir(root+"/zfs-state", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin+"/zfs", []byte(`#!/bin/sh
 echo "zfs $*" >> "$INSTALL_TEST_LOG"
 case "$1" in
 list)
   case "$*" in
     *v1@base) test -e "$INSTALL_ZFS_STATE/base";;
     *current) test -e "$INSTALL_ZFS_STATE/current";;
     *v1) test -e "$INSTALL_ZFS_STATE/version";;
     *) test -e "$INSTALL_ZFS_STATE/os";;
   esac;;
 create)
   case "$*" in *v1) touch "$INSTALL_ZFS_STATE/version";; *) touch "$INSTALL_ZFS_STATE/os";; esac;;
 snapshot) touch "$INSTALL_ZFS_STATE/base";;
 clone) touch "$INSTALL_ZFS_STATE/current";;
 get) echo floatlab/os/v1@base;;
 *) exit 0;;
 esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	p := Payload{Version: "v1", Init: "/nix/store/example/init", RootBytes: GiB, Files: map[string]string{}}
	for _, name := range []string{"floatlab-root.tar.gz", "zfsbootmenu.EFI"} {
		data := []byte("test " + name)
		if name == "floatlab-root.tar.gz" {
			data = rootArchive(t, map[string]string{"nix/store/example/init": "test init", "boot/vmlinuz": "test kernel", "boot/initramfs": "test initrd", "nix-path-registration": "test registration"})
		}
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
	// Storage installation runs while the user is still entering host settings.
	op.Identity.NodeName, op.Identity.Timezone = "", ""
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
	t.Setenv("INSTALL_FAIL_BOOTLOADER", "1")
	for attempt := 0; attempt < 3; attempt++ {
		if err := worker(context.Background(), runtimeDir, persistentDir, mediaDir, inventory); err == nil {
			t.Fatal("failed stack reported success")
		} else {
			t.Logf("attempt %d: %v", attempt, err)
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
		t.Setenv("INSTALL_FAIL_BOOTLOADER", "")
		inventory = func(context.Context) ([]Disk, error) { t.Fatal("retry must not inspect/erase disks"); return nil, nil }
	}
	commands, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"wipefs --all", "sfdisk --wipe", "zpool create -f"} {
		if strings.Count(string(commands), command) != 1 {
			t.Fatalf("destructive work repeated: %s\n%s", command, commands)
		}
	}
	if strings.Count(string(commands), "zbm-kcl -r") != 2 || strings.Count(string(commands), "umount "+runtimeDir+"/os") != 2 || strings.Count(string(commands), "umount "+runtimeDir+"/efi-0") != 2 {
		t.Fatal("image-copy retry leaked mounts", string(commands))
	}
	if strings.Contains(string(commands), "reboot") {
		t.Fatal("failed installation rebooted")
	}
	if dry, create := strings.Index(string(commands), "zpool create -n"), strings.Index(string(commands), "zpool create -f"); dry < 0 || create < dry {
		t.Fatal("pool creation did not follow a partition dry-run", string(commands))
	}
	// A checkpoint before storage is ready may never replay destructive work.
	if err := credentials.WritePrivate(runtimeDir+"/status.json", Status{Started: time.Now(), Phase: "Partition and format disks"}); err != nil {
		t.Fatal(err)
	}
	if err := worker(context.Background(), runtimeDir, persistentDir, mediaDir, inventory); err == nil || !strings.Contains(err.Error(), "interrupted destructive") {
		t.Fatal(err)
	}
	written := filepath.Join(runtimeDir, "os", "boot", "vmlinuz")
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

func TestCopyProgressCountsWrittenBytesAndVerifiesDestination(t *testing.T) {
	root := t.TempDir()
	data := []byte(strings.Repeat("Floatlab image\n", 10000))
	if err := os.WriteFile(root+"/source", data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	var copied int
	if err := copyVerifiedProgress(root+"/source", root+"/target", hex.EncodeToString(sum[:]), func(n int) { copied += n }); err != nil {
		t.Fatal(err)
	}
	if copied != len(data) {
		t.Fatalf("reported %d bytes for %d-byte image", copied, len(data))
	}
	written, err := os.ReadFile(root + "/target")
	if err != nil || string(written) != string(data) {
		t.Fatal("destination differs", err)
	}
}

func TestFrontendLossFinishesActiveCommandAndStopsBeforePoolCreation(t *testing.T) {
	root := t.TempDir()
	runtimeDir, persistentDir, mediaDir, bin := root+"/run", root+"/etc", root+"/media", root+"/bin"
	for _, dir := range []string{runtimeDir, persistentDir, mediaDir, bin} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	log, entered, finished := root+"/commands", root+"/entered", root+"/finished"
	for _, name := range []string{"wipefs", "sfdisk", "udevadm", "mkfs.vfat", "zpool"} {
		script := "#!/bin/sh\necho " + name + " >> " + log + "\n"
		if name == "wipefs" {
			script += "touch " + entered + "\nsleep 0.1\ntouch " + finished + "\n"
		}
		if err := os.WriteFile(bin+"/"+name, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	payload := Payload{Version: "v1", Init: "/nix/store/example/init", RootBytes: GiB, Bytes: 3, Files: map[string]string{"floatlab-root.tar.gz": strings.Repeat("a", 64), "zfsbootmenu.EFI": strings.Repeat("b", 64)}}
	if err := credentials.WritePrivate(mediaDir+"/floatlab-manifest.json", payload); err != nil {
		t.Fatal(err)
	}
	disks := testDisks()[:1]
	if err := credentials.WritePrivate(runtimeDir+"/operation.json", Operation{Disks: disks, Command: ZPoolCommand(disks)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			if _, err := os.Stat(entered); err == nil {
				cancel()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}()
	err := installWorker(ctx, runtimeDir, persistentDir, mediaDir, func(context.Context) ([]Disk, error) { return disks, nil }, nil)
	<-stopped
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(finished); err != nil {
		t.Fatal("active wipe command was killed", err)
	}
	commands, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(commands)) != "wipefs" {
		t.Fatal("new operation began after frontend loss", string(commands), err)
	}
}
