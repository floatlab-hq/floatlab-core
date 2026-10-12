package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/shlex"
)

const GiB int64 = 1 << 30
const MiB int64 = 1 << 20

type Disk struct {
	Path           string   `json:"path"`
	Device         string   `json:"device"`
	Model          string   `json:"model"`
	Serial         string   `json:"serial"`
	Size           int64    `json:"size"`
	Fingerprint    string   `json:"fingerprint"`
	PartitionIDs   []string `json:"partition_ids,omitempty"`
	PartitionTable string   `json:"partition_table"`
	Eligible       bool     `json:"eligible"`
	Reason         string   `json:"reason,omitempty"`
}
type block struct {
	Name        string   `json:"name"`
	KName       string   `json:"kname"`
	Type        string   `json:"type"`
	Size        int64    `json:"size"`
	Model       string   `json:"model"`
	Serial      string   `json:"serial"`
	RO          bool     `json:"ro"`
	Mountpoints []string `json:"mountpoints"`
	Children    []block  `json:"children"`
}

func Output(ctx context.Context, cmd string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, cmd, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w: %s", cmd, err, Diagnostic(out))
	}
	return out, nil
}
func occupied(b block, active string) bool {
	if b.RO || strings.Contains(active, b.Name+" ") || strings.Contains(active, b.Name+"\n") {
		return true
	}
	for _, m := range b.Mountpoints {
		if m != "" {
			return true
		}
	}
	holders, err := os.ReadDir("/sys/class/block/" + filepath.Base(b.KName) + "/holders")
	if err != nil || len(holders) > 0 {
		return true
	}
	for _, child := range b.Children {
		if occupied(child, active) {
			return true
		}
	}
	return false
}
func Inventory(ctx context.Context) ([]Disk, error) {
	out, err := Output(ctx, "lsblk", "--json", "--bytes", "--paths", "--output", "NAME,KNAME,TYPE,SIZE,MODEL,SERIAL,RO,MOUNTPOINTS")
	if err != nil {
		return nil, err
	}
	var tree struct {
		Devices []block `json:"blockdevices"`
	}
	if err = json.Unmarshal(out, &tree); err != nil {
		return nil, err
	}
	poolOut, poolErr := exec.CommandContext(ctx, "zpool", "status", "-LP").CombinedOutput()
	if poolErr != nil && !strings.Contains(string(poolOut), "no pools available") {
		return nil, fmt.Errorf("cannot inspect active ZFS pools")
	}
	ids, err := filepath.Glob("/dev/disk/by-id/*")
	if err != nil {
		return nil, err
	}
	sort.Strings(ids)
	var disks []Disk
	for _, b := range tree.Devices {
		if b.Type != "disk" {
			continue
		}
		d := Disk{Device: b.Name, Model: strings.TrimSpace(b.Model), Serial: strings.TrimSpace(b.Serial), Size: b.Size, Eligible: true}
		for _, id := range ids {
			target, e := filepath.EvalSymlinks(id)
			if e == nil && target == b.Name && !strings.Contains(filepath.Base(id), "-part") {
				d.Path = id
				break
			}
		}
		if d.Path == "" {
			d.Eligible = false
			d.Reason = "No stable disk identifier"
		}
		if occupied(b, string(poolOut)) {
			d.Eligible = false
			d.Reason = "Read-only, mounted, or in use (including installation media)"
		}
		table, e := exec.CommandContext(ctx, "sfdisk", "--dump", b.Name).CombinedOutput()
		if e != nil {
			if len(b.Children) > 0 {
				d.Eligible = false
				d.Reason = "Cannot inspect partition table"
			}
			table = []byte("No partition table\n")
		}
		d.PartitionTable = string(table)
		snapshot, _ := json.Marshal(struct {
			Block block
			Table string
		}{b, d.PartitionTable})
		sum := sha256.Sum256(snapshot)
		d.Fingerprint = hex.EncodeToString(sum[:])
		disks = append(disks, d)
	}
	return disks, nil
}

type Payload struct {
	Kernel string            `json:"kernel"`
	Initrd string            `json:"initrd"`
	Init   string            `json:"init"`
	Files  map[string]string `json:"files"`
	Bytes  int64             `json:"bytes"`
}

func (p Payload) Validate() error {
	if p.Bytes <= 0 || !strings.HasPrefix(p.Init, "/nix/store/") || strings.ContainsAny(p.Init, " \n\r'\";") {
		return fmt.Errorf("invalid image manifest")
	}
	for name, hash := range p.Files {
		if filepath.IsAbs(name) || filepath.Clean(name) != name || name == "." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, " \n\r'\";") || len(hash) != 64 {
			return fmt.Errorf("invalid payload path or checksum")
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return err
		}
	}
	for _, name := range []string{p.Kernel, p.Initrd, "nix-store.squashfs"} {
		if _, ok := p.Files[name]; !ok {
			return fmt.Errorf("manifest missing %s", name)
		}
	}
	return nil
}
func ImageSize(payloadBytes int64) int64 {
	size := ((payloadBytes + 2*GiB + GiB - 1) / GiB) * GiB
	if size < 8*GiB {
		size = 8 * GiB
	}
	return size
}
func DataPartition(d Disk) string { return d.Path + "-part3" }
func ZPoolArgs(disks []Disk) []string {
	args := []string{"create", "-f", "-o", "cachefile=none", "-o", "ashift=12", "-o", "autotrim=on", "-O", "compression=zstd", "-O", "atime=off", "-O", "mountpoint=none", "floatlab"}
	if len(disks) > 1 {
		args = append(args, "mirror")
	}
	for _, d := range disks {
		args = append(args, DataPartition(d))
	}
	return args
}
func ZPoolCommand(disks []Disk) string { return "zpool " + strings.Join(ZPoolArgs(disks), " ") }

// ParseZPool accepts exactly the generated command with only four property values editable.
func ParseZPool(command string, disks []Disk) ([]string, error) {
	words, err := shlex.Split(command)
	if err != nil {
		return nil, err
	}
	expected := append([]string{"zpool"}, ZPoolArgs(disks)...)
	if len(words) != len(expected) {
		return nil, fmt.Errorf("only ashift, autotrim, compression, and atime may be edited")
	}
	allowed := map[string]map[string]bool{"ashift": {"12": true, "13": true}, "autotrim": {"on": true, "off": true}, "compression": {"zstd": true, "lz4": true, "off": true}, "atime": {"on": true, "off": true}}
	for i, w := range words {
		if w == expected[i] {
			continue
		}
		key, _, ok := strings.Cut(expected[i], "=")
		k, value, valid := strings.Cut(w, "=")
		if !ok || !valid || k != key || !allowed[k][value] {
			return nil, fmt.Errorf("unsupported edit to %q", expected[i])
		}
	}
	return words[1:], nil
}
func ValidateSelection(disks []Disk, payloadBytes int64) error {
	if len(disks) == 0 {
		return fmt.Errorf("select at least one disk")
	}
	seen := map[string]bool{}
	for _, d := range disks {
		if !d.Eligible || !strings.HasPrefix(d.Path, "/dev/disk/by-id/") || strings.ContainsAny(d.Path, " \t\n\r'\";$`\\") || d.Fingerprint == "" || seen[d.Device] {
			return fmt.Errorf("invalid or duplicate disk %s", d.Path)
		}
		seen[d.Device] = true
		if d.Size < ImageSize(payloadBytes)+512*MiB+16*GiB+2*MiB {
			return fmt.Errorf("%s requires space for the image and at least 16 GiB of ZFS data", d.Path)
		}
	}
	return nil
}
func Revalidate(selected, current []Disk) error {
	for _, old := range selected {
		found := false
		for _, d := range current {
			if d.Path == old.Path {
				found = d.Eligible && d.Fingerprint == old.Fingerprint && d.Device == old.Device
				break
			}
		}
		if !found {
			return fmt.Errorf("disk %s changed; preview and erase confirmation must be repeated", old.Path)
		}
	}
	return nil
}
func PartitionScript(imageBytes int64) string {
	return "label: gpt\nunit: sectors\n\nsize=512MiB, type=U\nsize=" + strconv.FormatInt(imageBytes/MiB, 10) + "MiB, type=L\ntype=6A898CC3-1DD2-11B2-99A6-080020736631\n"
}

// Commands never receive credentials in argv. Still suppress secret-bearing output
// and terminal controls before displaying bounded diagnostics from external tools.
func Diagnostic(out []byte) string {
	text := regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`).ReplaceAllString(string(out), "")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lower := strings.ToLower(line)
		for _, word := range []string{"password", "secret", "token", "authorization", "otpauth", "recovery"} {
			if strings.Contains(lower, word) {
				lines[i] = "[redacted]"
				break
			}
		}
		lines[i] = strings.Map(func(r rune) rune {
			if r < 32 && r != '\t' {
				return -1
			}
			return r
		}, lines[i])
	}
	text = strings.TrimSpace(strings.Join(lines, "\n"))
	if len(text) > 2048 {
		text = text[len(text)-2048:]
	}
	return text
}

// Check every partition identity before resuming writes after a restart or disk swap.
func verifyImageTargets(ctx context.Context, disks []Disk, mediaDir string) error {
	source, err := Output(ctx, "findmnt", "-n", "-o", "SOURCE", "--target", mediaDir)
	if err != nil {
		return err
	}
	sourcePath := strings.TrimSpace(string(source))
	if resolved, e := filepath.EvalSymlinks(sourcePath); e == nil {
		sourcePath = resolved
	}
	for _, d := range disks {
		if len(d.PartitionIDs) != 3 {
			return fmt.Errorf("missing recorded partition identities; inspect installation manually")
		}
		imagePath := d.Path + "-part2"
		if resolved, e := filepath.EvalSymlinks(imagePath); e == nil {
			imagePath = resolved
		}
		if imagePath == sourcePath {
			return fmt.Errorf("boot the original installation ISO to resume image copying; the running image partition must remain read-only")
		}
		for i, id := range d.PartitionIDs {
			out, e := Output(ctx, "blkid", "-s", "PARTUUID", "-o", "value", fmt.Sprintf("%s-part%d", d.Path, i+1))
			if e != nil || strings.TrimSpace(string(out)) != id {
				return fmt.Errorf("partition identity changed on %s; refusing image writes", d.Path)
			}
		}
	}
	return nil
}
