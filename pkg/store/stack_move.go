package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const moveOperationProperty = "floatlab:move-operation"

var moveStackName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// MoveBackupDataset keeps the project root outside the per-mount backup hierarchy.
func MoveBackupDataset(source, root, stackID, dataset string) string {
	if dataset == root {
		return "floatlab/backups/" + source + "/move-projects/" + stackID
	}
	return BackupDataset(source, dataset)
}

func validateStackMove(operation, root, snapshot string, datasets []DatasetParams) error {
	name, ok := strings.CutPrefix(root, "floatlab/")
	if !ok || !moveStackName.MatchString(name) || name == "system" || name == "backups" || name == "restores" {
		return errors.New("move target must be a managed workload dataset")
	}
	if !backupID.MatchString(operation) || !IsAutomatedSnapshot(snapshot) || len(datasets) == 0 || datasets[0].Dataset != root {
		return errors.New("invalid move operation, final snapshot or dataset list")
	}
	seen := map[string]bool{root: true}
	for _, dataset := range datasets[1:] {
		mount, inside := strings.CutPrefix(dataset.Dataset, root+"/")
		if !inside || !validRestoreMount(mount) || seen[dataset.Dataset] {
			return fmt.Errorf("invalid or duplicate move mount %s", dataset.Dataset)
		}
		seen[dataset.Dataset] = true
	}
	return nil
}

func ActivateStackMove(ctx context.Context, operation, stackID, source, root, snapshot string, datasets []DatasetParams) error {
	if err := validateStackMove(operation, root, snapshot, datasets); err != nil {
		return err
	}
	if !backupID.MatchString(source) || !backupID.MatchString(stackID) {
		return errors.New("invalid move source or stack ID")
	}
	var paths []string
	for _, dataset := range datasets {
		paths = append(paths, dataset.Dataset, MoveBackupDataset(source, root, stackID, dataset.Dataset))
	}
	unlock, err := lockMoveDatasets(paths)
	if err != nil {
		return err
	}
	defer unlock()
	return activateStackMoveLocked(ctx, operation, stackID, source, root, snapshot, datasets)
}

func activateStackMoveLocked(ctx context.Context, operation, stackID, source, root, snapshot string, datasets []DatasetParams) error {
	backups := make([]string, len(datasets))
	for i, dataset := range datasets {
		target := dataset.Dataset
		if _, err := run(ctx, "zfs", "list", "-H", "-o", "name", target); err == nil {
			if err := verifyMoveProperty(ctx, target, moveOperationProperty, operation); err != nil {
				return err
			}
			if err := verifyMoveSnapshot(ctx, target, snapshot, false); err != nil {
				return err
			}
			continue
		}
		backup := MoveBackupDataset(source, root, stackID, target)
		if err := verifyMoveProperty(ctx, backup, "floatlab:backup-source", source); err != nil {
			return err
		}
		if err := verifyMoveTree(ctx, backup, []string{backup}); err != nil {
			return err
		}
		if err := verifyMoveSnapshot(ctx, backup, snapshot, true); err != nil {
			return err
		}
		backups[i] = backup
	}
	for i, dataset := range datasets {
		target := dataset.Dataset
		if backup := backups[i]; backup != "" {
			if _, err := run(ctx, "zfs", "set", moveOperationProperty+"="+operation, backup); err != nil {
				return err
			}
			if _, err := run(ctx, "zfs", "rename", backup, target); err != nil {
				return err
			}
		}
		args := []string{"set", "readonly=off", "mountpoint=/" + target, "canmount=on"}
		for property, value := range map[string]string{"recordsize": dataset.BlockSize, "compression": dataset.Compression, "quota": dataset.Quota} {
			if value != "" {
				args = append(args, property+"="+value)
			}
		}
		if _, err := run(ctx, "zfs", append(args, target)...); err != nil {
			return err
		}
		mounted, err := run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", target)
		if err != nil {
			return err
		}
		if mounted != "yes" {
			if _, err := run(ctx, "zfs", "mount", target); err != nil {
				return err
			}
		}
	}
	return nil
}

func ArchiveStackMove(ctx context.Context, operation, root, snapshot string, datasets []DatasetParams) error {
	if err := validateStackMove(operation, root, snapshot, datasets); err != nil {
		return err
	}
	paths := []string{"floatlab/restores/moves/" + operation}
	for _, dataset := range datasets {
		paths = append(paths, dataset.Dataset)
	}
	unlock, err := lockMoveDatasets(paths)
	if err != nil {
		return err
	}
	defer unlock()
	return archiveStackMoveLocked(ctx, operation, root, snapshot, datasets)
}

func archiveStackMoveLocked(ctx context.Context, operation, root, snapshot string, datasets []DatasetParams) error {
	archive := "floatlab/restores/moves/" + operation
	location := root
	if _, err := run(ctx, "zfs", "list", "-H", "-o", "name", archive); err == nil {
		if err := verifyMoveProperty(ctx, archive, moveOperationProperty, operation); err != nil {
			return err
		}
		location = archive
	}
	paths := make([]string, 0, len(datasets))
	for _, dataset := range datasets {
		paths = append(paths, location+strings.TrimPrefix(dataset.Dataset, root))
	}
	if err := verifyMoveTree(ctx, location, paths); err != nil {
		return err
	}
	for _, path := range paths {
		if err := verifyMoveSnapshot(ctx, path, snapshot, false); err != nil {
			return err
		}
	}
	if location == archive {
		return nil
	}
	if _, err := run(ctx, "zfs", "set", moveOperationProperty+"="+operation, root); err != nil {
		return err
	}
	// Unmount children first, before disabling the parent mount.
	for i := len(paths) - 1; i >= 0; i-- {
		if _, err := run(ctx, "zfs", "set", "mountpoint=none", "canmount=off", "readonly=on", paths[i]); err != nil {
			return err
		}
		mounted, err := run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", paths[i])
		if err != nil || mounted != "no" {
			return errors.Join(fmt.Errorf("move source dataset %s is still mounted", paths[i]), err)
		}
	}
	_, err := run(ctx, "zfs", "rename", "-p", root, archive)
	return err
}

func lockMoveDatasets(paths []string) (func(), error) {
	slices.Sort(paths)
	var locks []func()
	unlock := func() {
		for _, release := range locks {
			release()
		}
	}
	for _, dataset := range paths {
		release, err := lockBackupDataset(dataset)
		if err != nil {
			unlock()
			return nil, err
		}
		locks = append(locks, release)
	}
	return unlock, nil
}

func verifyMoveProperty(ctx context.Context, dataset, property, expected string) error {
	value, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", property, dataset)
	if err != nil || value != expected {
		return errors.Join(fmt.Errorf("move dataset %s has unexpected %s", dataset, property), err)
	}
	return nil
}

func verifyMoveTree(ctx context.Context, root string, paths []string) error {
	list, err := run(ctx, "zfs", "list", "-H", "-r", "-t", "filesystem,volume", "-o", "name", root)
	if err != nil {
		return err
	}
	actual := strings.Fields(list)
	expected := slices.Clone(paths)
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return fmt.Errorf("move dataset %s does not match the configured dataset tree", root)
	}
	return nil
}

func verifyMoveSnapshot(ctx context.Context, dataset, snapshot string, requireLatest bool) error {
	token, err := run(ctx, "zfs", "get", "-H", "-o", "value", "receive_resume_token", dataset)
	if err != nil || (token != "-" && token != "") {
		return errors.Join(fmt.Errorf("move dataset %s has incomplete receive state", dataset), err)
	}
	list, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-s", "createtxg", "-d", "1", dataset)
	if err != nil {
		return err
	}
	snapshots := parseBackupSnapshots(list, dataset)
	for i, point := range snapshots {
		if point.Name == snapshot && (!requireLatest || i == len(snapshots)-1) {
			return nil
		}
	}
	return fmt.Errorf("move dataset %s does not contain the expected final snapshot %s", dataset, snapshot)
}
