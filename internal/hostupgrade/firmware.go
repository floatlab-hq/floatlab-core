package hostupgrade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/floatlab/floatlab-core/internal/update"
)

var bootEntry = regexp.MustCompile(`(?mi)^Boot([0-9A-F]{4})\*?[\t ]+FloatLab Trial[\t ]+HD\(1,GPT,([0-9a-f-]{36}),[^)\r\n]*\)/(?:File\(\\EFI\\FloatLab\\TRIAL\.EFI\)|\\EFI\\FloatLab\\TRIAL\.EFI)[\t ]*$`)

func (l Linux) Arm(ctx context.Context, s Status) error {
	var installation installer.Operation
	b, err := os.ReadFile("/floatlab/system/etc/install-operation.json")
	if err != nil {
		return fmt.Errorf("installed ESP identities unavailable: %w", err)
	}
	if err = json.Unmarshal(b, &installation); err != nil {
		return err
	}
	if len(installation.Disks) == 0 {
		return fmt.Errorf("no installed ESPs")
	}
	for i, disk := range installation.Disks {
		if !strings.HasPrefix(disk.Path, "/dev/disk/by-id/") || strings.ContainsAny(disk.Path, "\n\r") || len(disk.PartitionIDs) != 2 {
			return fmt.Errorf("invalid installed disk record")
		}
		id, e := l.output(ctx, "blkid", "-s", "PARTUUID", "-o", "value", disk.Path+"-part1")
		if e != nil || id != disk.PartitionIDs[0] {
			return fmt.Errorf("ESP identity changed on %s", disk.Path)
		}
		efi := filepath.Join("/run/floatlab-updates", s.OperationID, fmt.Sprintf("efi-%d", i))
		if err = os.MkdirAll(efi, 0700); err != nil {
			return err
		}
		if err = l.run(ctx, "mount", "-o", "nodev,nosuid,noexec", disk.Path+"-part1", efi); err != nil {
			return err
		}
		err = func() (e error) {
			defer func() { e = errors.Join(e, l.run(context.WithoutCancel(ctx), "umount", efi)) }()
			original, e := os.ReadFile(efi + "/EFI/BOOT/BOOTX64.EFI")
			if e != nil {
				return e
			}
			target := efi + "/EFI/FloatLab/TRIAL.EFI"
			if e = update.WriteFile(target, original, 0600); e != nil {
				return e
			}
			if e = l.run(ctx, "zbm-kcl", "-r", "zbm.prefer", "-a", "zbm.prefer=floatlab/os/next!!", target); e != nil {
				return e
			}
			cmdline, e := l.output(ctx, "zbm-kcl", target)
			if e != nil || !strings.Contains(cmdline, "zbm.prefer=floatlab/os/next!!") {
				return fmt.Errorf("trial EFI preference verification failed")
			}
			return l.run(ctx, "sync", "-f", target)
		}()
		if err != nil {
			return err
		}
		if _, err = l.ensureTrialEntry(ctx, disk.Path, disk.PartitionIDs[0]); err != nil {
			return err
		}
	}
	entries, err := l.output(ctx, "efibootmgr", "-v")
	if err != nil {
		return err
	}
	matches := bootEntry.FindAllStringSubmatch(entries, -1)
	if len(matches) == 0 {
		return fmt.Errorf("firmware did not publish a usable trial EFI entry")
	}
	chosen := ""
	for _, disk := range installation.Disks {
		verified := ""
		for _, entry := range matches {
			if strings.EqualFold(entry[2], disk.PartitionIDs[0]) {
				verified = entry[1]
			}
		}
		if verified == "" {
			return fmt.Errorf("firmware trial entry missing for installed ESP %s", disk.PartitionIDs[0])
		}
		chosen = verified
	}
	if err = l.run(ctx, "efibootmgr", "--bootnext", chosen); err != nil {
		return err
	}
	actual, err := l.output(ctx, "efibootmgr")
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToUpper(actual), "BOOTNEXT: "+strings.ToUpper(chosen)) {
		return fmt.Errorf("firmware BootNext did not persist")
	}
	return nil
}

// Reuse the exact ESP/loader tuple; activation never grows firmware NVRAM indefinitely.
func (l Linux) ensureTrialEntry(ctx context.Context, disk, partitionID string) (string, error) {
	entries, err := l.output(ctx, "efibootmgr", "-v")
	if err != nil {
		return "", err
	}
	find := func(text string) string {
		for _, entry := range bootEntry.FindAllStringSubmatch(text, -1) {
			if strings.EqualFold(entry[2], partitionID) {
				return entry[1]
			}
		}
		return ""
	}
	if existing := find(entries); existing != "" {
		return existing, nil
	}
	if err = l.run(ctx, "efibootmgr", "--create-only", "--disk", disk, "--part", "1", "--label", "FloatLab Trial", "--loader", `\EFI\FloatLab\TRIAL.EFI`); err != nil {
		return "", err
	}
	entries, err = l.output(ctx, "efibootmgr", "-v")
	if err != nil {
		return "", err
	}
	entry := find(entries)
	if entry == "" {
		return "", fmt.Errorf("firmware did not register exact ESP trial loader")
	}
	return entry, nil
}
