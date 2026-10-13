package installer

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

func testDisks() []Disk {
	return []Disk{{Path: "/dev/disk/by-id/virtio-a", Device: "/dev/vda", Size: 32 * GiB, Eligible: true, Fingerprint: "a"}, {Path: "/dev/disk/by-id/virtio-b", Device: "/dev/vdb", Size: 40 * GiB, Eligible: true, Fingerprint: "b"}}
}
func TestStorageCommandAndSafety(t *testing.T) {
	if got := PartitionScript(); strings.Count(got, "type=") != 2 || strings.Contains(got, "type=L") {
		t.Fatal("expected only EFI and ZFS partitions", got)
	}
	disks := testDisks()
	command := ZPoolCommand(disks)
	if !strings.Contains(command, "floatlab mirror /dev/disk/by-id/virtio-a") {
		t.Fatal(command)
	}
	if _, err := ParseZPool(strings.Replace(command, "compression=zstd", "compression=lz4", 1), disks); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{command + "; reboot", strings.Replace(command, "virtio-a", "virtio-c", 1), strings.Replace(command, "ashift=12", "ashift=9", 1), strings.Replace(command, "mountpoint=none", "mountpoint=/", 1)} {
		if _, err := ParseZPool(bad, disks); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	if err := ValidateSelection(disks, GiB); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSelection(append(disks, disks[0]), GiB); err == nil {
		t.Fatal("duplicate accepted")
	}
	disks[0].Eligible = false
	if err := ValidateSelection(disks, GiB); err == nil {
		t.Fatal("occupied disk accepted")
	}
	disks = testDisks()
	disks[0].Size = 20 * GiB
	if err := ValidateSelection(disks, GiB); err == nil {
		t.Fatal("undersized disk accepted")
	}
	current := testDisks()
	current[0].Fingerprint = "changed"
	if err := Revalidate(testDisks(), current); err == nil {
		t.Fatal("changed partition table accepted")
	}
	current = testDisks()
	current[1].Eligible = false
	if err := Revalidate(testDisks(), current); err == nil {
		t.Fatal("newly occupied disk accepted")
	}
	if ImageSize(GiB) != 8*GiB || ImageSize(9*GiB+1) != 12*GiB {
		t.Fatal("image sizing")
	}
}

func TestEditableTopologyMapsEveryDiskToItsZFSPartition(t *testing.T) {
	disks := testDisks()
	command := "zpool create -o ashift=12 floatlab raidz2 /dev/vda /dev/disk/by-id/virtio-b"
	args, err := ParseZPool(command, disks)
	if err != nil {
		t.Fatal(err)
	}
	final := strings.Join(args, " ")
	if !strings.Contains(final, "floatlab raidz2 "+DataPartition(disks[0])+" "+DataPartition(disks[1])) {
		t.Fatal(final)
	}
	whole := strings.Join(wholeDiskArgs(args, disks), " ")
	if strings.Contains(whole, "-part2") || !strings.Contains(whole, disks[0].Path) {
		t.Fatal(whole)
	}
	for _, invalid := range []string{
		"zpool create -o compatibility=off floatlab /dev/vda",
		"zpool create floatlab /dev/vda /dev/disk/by-id/virtio-a",
		"zpool create floatlab /tmp/file-vdev",
		"zpool create -o cachefile=/etc/zfs/zpool.cache floatlab /dev/vda",
		"zpool create -O mountpoint=/ floatlab /dev/vda",
		"zpool create floatlab /dev/vda | reboot",
		"zpool create floatlab $(whoami)",
		"zpool create floatlab /dev/vda\nzpool create another /dev/vdb",
	} {
		if _, err := ParseZPool(invalid, disks); err == nil {
			t.Fatal("unsafe command accepted", invalid)
		}
	}
}
func TestSettingsAndBootPayload(t *testing.T) {
	a := Appliance{NodeID: "node", NodeName: "floatlab", Timezone: "UTC", HostID: "abcdef12", JWTSecret: strings.Repeat("a", 64), Network: hostnetwork.DefaultConfig()}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.Timezone = "Australia/Melbourne"
	if err := a.Validate(); err != nil {
		t.Fatal("non-UTC timezone unavailable", err)
	}
	a.NodeName = "bad name"
	if err := a.Validate(); err == nil {
		t.Fatal("invalid hostname")
	}
	a.NodeName = "floatlab"
	a.Timezone = "../../etc/passwd"
	if err := a.Validate(); err == nil {
		t.Fatal("timezone traversal")
	}
	p := Payload{Version: "v1", Init: "/nix/store/example/init", RootBytes: GiB, Bytes: 100, Files: map[string]string{"floatlab-root.tar.gz": strings.Repeat("a", 64), "zfsbootmenu.EFI": strings.Repeat("b", 64)}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"../escape", "current", "live", "next", "v1@base", ""} {
		bad := p
		bad.Version = version
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted reserved or unsafe version %q", version)
		}
	}
	p.Files["../escape"] = strings.Repeat("a", 64)
	if err := p.Validate(); err == nil {
		t.Fatal("payload traversal")
	}
}

func TestDiagnosticsRedactSecrets(t *testing.T) {
	out := Diagnostic([]byte("\x1b[31mDisk failed\x1b[0m\npassword=bad\nTOKEN sensitive\nTry another disk\n"))
	if strings.Contains(out, "bad") || strings.Contains(out, "sensitive") || strings.Contains(out, "\x1b") || !strings.Contains(out, "Disk failed") {
		t.Fatal(out)
	}
	if len(Diagnostic([]byte(strings.Repeat("x", 4096)))) > 2048 {
		t.Fatal("unbounded diagnostics")
	}
}

func TestImageRecoveryProtectsRunningMedia(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	disks := testDisks()[:1]
	disks[0].PartitionIDs = []string{"a", "b"}
	script := "#!/bin/sh\necho " + disks[0].Path + "-part2\n"
	if err := os.WriteFile(root+"/findmnt", []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := verifyImageTargets(context.Background(), disks, "/iso"); err == nil || !strings.Contains(err.Error(), "original installation ISO") {
		t.Fatal("allowed writes to running image", err)
	}
}
