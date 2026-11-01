package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const restoreOperationProperty = "floatlab:restore-operation"
const restoreGUIDProperty = "floatlab:restore-guid"
const restoreSourceProperty = "floatlab:restore-source"
const restoreMountProperty = "floatlab:restore-mount"

type BackupRestorePrepareRequest struct {
	SourceNodeID, BackupNodeID, Dataset, Snapshot, OperationID, MountID string
	DestNodeID, DestHost                                                string
	DestPort                                                            int
	IdentityFile, KnownHostsFile                                        string
}

type BackupRestorePrepareResult struct {
	Dataset  string `json:"dataset"`
	Snapshot string `json:"snapshot"`
	GUID     string `json:"guid"`
}

type BackupRestoreActivateRequest struct {
	Dataset, SourceNodeID, OperationID, MountID, TargetDataset, Mountpoint, ExpectedGUID string
}

type restoreMetadata struct {
	OperationID string `json:"operation_id"`
	MountID     string `json:"mount_id"`
	Snapshot    string `json:"snapshot"`
	GUID        string `json:"guid"`
}

func RestoreDataset(pool, source, operation, mount string) string {
	return pool + "/restores/" + source + "/" + operation + "/" + mount
}

func validRestoreMetadata(m restoreMetadata) bool {
	return backupID.MatchString(m.OperationID) && validRestoreMount(m.MountID) && IsAutomatedSnapshot(m.Snapshot) && regexp.MustCompile(`^[0-9]+$`).MatchString(m.GUID)
}

func validRestoreMount(mount string) bool {
	return regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(mount) && mount != "." && mount != ".."
}

func validRestoreStage(dataset, source, operation, mount string) bool {
	parts := strings.Split(dataset, "/")
	return validBackupDataset(dataset) && len(parts) == 5 && backupID.MatchString(source) && backupID.MatchString(operation) && validRestoreMount(mount) && dataset == RestoreDataset(parts[0], source, operation, mount)
}

// PrepareBackupRestore always writes to operation-owned staging, never a workload or backup.
func PrepareBackupRestore(ctx context.Context, r BackupRestorePrepareRequest) (*BackupRestorePrepareResult, error) {
	if !validBackupDataset(r.Dataset) || !backupID.MatchString(r.SourceNodeID) || !backupID.MatchString(r.BackupNodeID) || !backupID.MatchString(r.OperationID) || !validRestoreMount(r.MountID) || !IsAutomatedSnapshot(r.Snapshot) {
		return nil, errors.New("invalid backup restore identity")
	}
	if r.DestNodeID != "" && r.DestNodeID != r.BackupNodeID {
		if err := normalizeRestoreDestination(&r); err != nil {
			return nil, err
		}
	}
	backup := BackupDataset(r.SourceNodeID, r.Dataset)
	unlock, err := lockBackupDataset(backup)
	if err != nil {
		return nil, err
	}
	defer unlock()
	owner, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", "floatlab:backup-source", backup)
	if err != nil || owner != r.SourceNodeID {
		return nil, errors.Join(errors.New("backup restore requires an owned backup dataset"), err)
	}
	guid, err := run(ctx, "zfs", "get", "-H", "-o", "value", "guid", backup+"@"+r.Snapshot)
	if err != nil {
		return nil, err
	}
	m := restoreMetadata{r.OperationID, r.MountID, r.Snapshot, guid}
	if !validRestoreMetadata(m) {
		return nil, errors.New("invalid backup snapshot GUID")
	}
	stage := RestoreDataset(strings.Split(r.Dataset, "/")[0], r.BackupNodeID, r.OperationID, r.MountID)
	tag := "floatlab:restore:" + r.OperationID + ":" + r.MountID
	tags, err := snapshotHolds(ctx, backup+"@"+r.Snapshot)
	if err != nil {
		return nil, err
	}
	if !tags[tag] {
		if _, err = run(ctx, "zfs", "hold", tag, backup+"@"+r.Snapshot); err != nil {
			return nil, err
		}
	}
	if r.DestNodeID == "" || r.DestNodeID == r.BackupNodeID {
		unlockStage, e := lockBackupDataset(stage)
		if e != nil {
			return nil, e
		}
		err = prepareLocalRestore(ctx, backup, stage, r.BackupNodeID, m)
		unlockStage()
	} else {
		err = prepareRemoteRestore(ctx, r, backup, stage, m)
	}
	if err != nil {
		// Failed resumable transfers retain the exact source snapshot for the next attempt.
		return nil, err
	}
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if _, err = run(release, "zfs", "release", tag, backup+"@"+r.Snapshot); err != nil {
		return nil, err
	}
	return &BackupRestorePrepareResult{stage, r.Snapshot, guid}, nil
}

func restoreProperties(source string, m restoreMetadata) []string {
	return []string{"-o", "mountpoint=none", "-o", "readonly=on", "-o", restoreOperationProperty + "=" + m.OperationID, "-o", restoreGUIDProperty + "=" + m.GUID, "-o", restoreSourceProperty + "=" + source, "-o", restoreMountProperty + "=" + m.MountID}
}

func verifyRestoreOwnership(ctx context.Context, dataset, source string, m restoreMetadata) error {
	for property, expected := range map[string]string{restoreOperationProperty: m.OperationID, restoreGUIDProperty: m.GUID, restoreSourceProperty: source, restoreMountProperty: m.MountID} {
		value, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", property, dataset)
		if err != nil {
			return err
		}
		if value != expected {
			return fmt.Errorf("restore ownership mismatch for %s", dataset)
		}
	}
	return nil
}

func prepareLocalRestore(ctx context.Context, backup, stage, source string, m restoreMetadata) error {
	if _, err := run(ctx, "zfs", "list", "-H", "-o", "name", stage); err == nil {
		if err = verifyRestoreOwnership(ctx, stage, source, m); err != nil {
			return err
		}
		origin, err := run(ctx, "zfs", "get", "-H", "-o", "value", "origin", stage)
		if err != nil {
			return err
		}
		if origin != backup+"@"+m.Snapshot {
			return errors.New("restore clone origin mismatch")
		}
		return nil
	}
	args := append([]string{"clone", "-p"}, restoreProperties(source, m)...)
	args = append(args, backup+"@"+m.Snapshot, stage)
	_, err := run(ctx, "zfs", args...)
	return err
}

type restoreTransport struct {
	commandBackup
	metadata restoreMetadata
}

func (b *restoreTransport) remote(action string) []string {
	args := b.remoteArgs(action)
	encoded, _ := json.Marshal(b.metadata)
	args[len(args)-4] = "zfs-restore"
	args[len(args)-1] = base64.RawURLEncoding.EncodeToString(encoded)
	return args
}

func (b *restoreTransport) state(ctx context.Context) (BackupState, error) {
	out, err := run(ctx, "ssh", b.remote("state")...)
	var state BackupState
	if err == nil {
		err = json.Unmarshal([]byte(out), &state)
	}
	return state, err
}

func normalizeRestoreDestination(r *BackupRestorePrepareRequest) error {
	if !backupID.MatchString(r.DestNodeID) {
		return errors.New("invalid restore destination node")
	}
	host := strings.Trim(r.DestHost, "[]")
	if net.ParseIP(host) == nil && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`).MatchString(host) {
		return errors.New("invalid restore destination host")
	}
	if r.DestPort == 0 {
		r.DestPort = 22
	}
	if r.DestPort < 1 || r.DestPort > 65535 {
		return errors.New("invalid restore destination port")
	}
	if r.IdentityFile == "" {
		r.IdentityFile = "/floatlab/system/workloads/lab/replication-key"
	}
	if r.KnownHostsFile == "" {
		r.KnownHostsFile = "/floatlab/system/workloads/lab/known_hosts"
	}
	r.DestHost = host
	return nil
}

func prepareRemoteRestore(ctx context.Context, r BackupRestorePrepareRequest, backup, stage string, m restoreMetadata) error {
	b := restoreTransport{commandBackup{ReplicationRequest{Dataset: backup, Snapshot: r.Snapshot, SourceNodeID: r.BackupNodeID, DestNodeID: r.DestNodeID, DestDataset: stage, DestHost: r.DestHost, DestPort: r.DestPort, IdentityFile: r.IdentityFile, KnownHostsFile: r.KnownHostsFile}}, m}
	state, err := b.state(ctx)
	if err != nil {
		return err
	}
	if state.ResumeToken != "" {
		if err = b.validateToken(ctx, state.ResumeToken, m.Snapshot); err != nil {
			if !errors.Is(err, ErrInvalidResumeToken) {
				return err
			}
			if _, err = run(ctx, "ssh", b.remote("abort")...); err != nil {
				return err
			}
		} else if _, err = sendBackupStream(ctx, []string{"send", "-t", state.ResumeToken}, b.remote("receive")); err != nil {
			return err
		}
		state, err = b.state(ctx)
		if err != nil {
			return err
		}
	}
	if state.ResumeToken != "" {
		return errors.New("restore receive remains incomplete")
	}
	if len(state.Snapshots) == 0 {
		if _, err = sendBackupStream(ctx, []string{"send", backup + "@" + m.Snapshot}, b.remote("receive")); err != nil {
			return err
		}
		state, err = b.state(ctx)
		if err != nil {
			return err
		}
	}
	if state.ResumeToken != "" || len(state.Snapshots) != 1 || state.Snapshots[0].Name != m.Snapshot || state.Snapshots[0].GUID != m.GUID {
		return errors.New("restore snapshot GUID verification failed")
	}
	return nil
}

func ActivateBackupRestore(ctx context.Context, r BackupRestoreActivateRequest) error {
	if !validRestoreStage(r.Dataset, r.SourceNodeID, r.OperationID, r.MountID) || !validBackupDataset(r.TargetDataset) || r.Mountpoint != "/"+r.TargetDataset {
		return errors.New("invalid restore activation")
	}
	parts := strings.Split(r.TargetDataset, "/")
	if len(parts) < 3 || parts[0] != strings.Split(r.Dataset, "/")[0] || parts[1] == "system" || parts[1] == "backups" || parts[1] == "restores" || !regexp.MustCompile(`^[0-9]+$`).MatchString(r.ExpectedGUID) {
		return errors.New("restore target must be a new workload dataset in the same pool")
	}
	unlock, err := lockBackupDataset(r.Dataset)
	if err != nil {
		return err
	}
	defer unlock()
	return activateBackupRestoreLocked(ctx, r)
}

func activateBackupRestoreLocked(ctx context.Context, r BackupRestoreActivateRequest) error {
	root := strings.Join(strings.Split(r.TargetDataset, "/")[:2], "/")
	owner, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", restoreOperationProperty, root)
	if err != nil || owner != r.OperationID {
		return errors.Join(errors.New("restore target stack is not reserved by this operation"), err)
	}
	m := restoreMetadata{OperationID: r.OperationID, MountID: r.MountID, GUID: r.ExpectedGUID}
	if _, err = run(ctx, "zfs", "list", "-H", "-o", "name", r.TargetDataset); err == nil {
		if err = verifyRestoreOwnership(ctx, r.TargetDataset, r.SourceNodeID, m); err != nil {
			return err
		}
		// A retried prepare may have recreated staging after activation already succeeded.
		if _, exists := run(ctx, "zfs", "list", "-H", "-o", "name", r.Dataset); exists == nil {
			if err = verifyRestoreOwnership(ctx, r.Dataset, r.SourceNodeID, m); err != nil {
				return err
			}
			if err = verifyRestoreData(ctx, r.Dataset, r.ExpectedGUID); err != nil {
				return err
			}
			if _, err = run(ctx, "zfs", "destroy", "-r", r.Dataset); err != nil {
				return err
			}
		}
	} else {
		if err = verifyRestoreOwnership(ctx, r.Dataset, r.SourceNodeID, m); err != nil {
			return err
		}
		if err = verifyRestoreData(ctx, r.Dataset, r.ExpectedGUID); err != nil {
			return err
		}
		if _, err = run(ctx, "zfs", "rename", "-p", r.Dataset, r.TargetDataset); err != nil {
			return err
		}
	}
	if _, err = run(ctx, "zfs", "set", "readonly=off", "mountpoint="+r.Mountpoint, r.TargetDataset); err != nil {
		return err
	}
	mounted, err := run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", r.TargetDataset)
	if err != nil {
		return err
	}
	if mounted != "yes" {
		if _, err = run(ctx, "zfs", "mount", r.TargetDataset); err != nil {
			return err
		}
		mounted, err = run(ctx, "zfs", "get", "-H", "-o", "value", "mounted", r.TargetDataset)
		if err != nil {
			return err
		}
		if mounted != "yes" {
			return errors.New("restored dataset is not mounted")
		}
	}
	return nil
}

func verifyRestoreData(ctx context.Context, dataset, guid string) error {
	token, err := run(ctx, "zfs", "get", "-H", "-o", "value", "receive_resume_token", dataset)
	if err != nil {
		return err
	}
	if token != "-" && token != "" {
		return errors.New("restore has an incomplete receive")
	}
	origin, err := run(ctx, "zfs", "get", "-H", "-o", "value", "origin", dataset)
	if err != nil {
		return err
	}
	if origin != "-" && origin != "" {
		actual, err := run(ctx, "zfs", "get", "-H", "-o", "value", "guid", origin)
		if err != nil {
			return err
		}
		if actual == guid {
			return nil
		}
	} else {
		listing, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-d", "1", dataset)
		if err != nil {
			return err
		}
		for _, snapshot := range parseBackupSnapshots(listing, dataset) {
			if snapshot.GUID == guid {
				return nil
			}
		}
	}
	return errors.New("restore staging data does not match requested snapshot GUID")
}

func ReserveBackupRestore(ctx context.Context, operation, target string) error {
	parts := strings.Split(target, "/")
	if !backupID.MatchString(operation) || !validBackupDataset(target) || len(parts) != 2 || parts[1] == "system" || parts[1] == "backups" || parts[1] == "restores" {
		return errors.New("invalid restore stack reservation")
	}
	unlock, err := lockBackupDataset(target)
	if err != nil {
		return err
	}
	defer unlock()
	return reserveBackupRestoreLocked(ctx, operation, target)
}

func reserveBackupRestoreLocked(ctx context.Context, operation, target string) error {
	if _, err := run(ctx, "zfs", "list", "-H", "-o", "name", target); err == nil {
		owner, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", restoreOperationProperty, target)
		if err != nil {
			return err
		}
		if owner != operation {
			return errors.New("restore target stack already exists")
		}
		return nil
	}
	_, err := run(ctx, "zfs", "create", "-o", restoreOperationProperty+"="+operation, "-o", "mountpoint=/"+target, target)
	return err
}

// ServeRestoreReceiver restricts streams to a single operation under the authenticated sender.
func ServeRestoreReceiver(ctx context.Context, source, command string, in io.Reader, out, stderr io.Writer) error {
	f := strings.Fields(command)
	if len(f) != 4 || f[0] != "zfs-restore" || (f[1] != "state" && f[1] != "abort" && f[1] != "receive") {
		return errors.New("invalid restore command")
	}
	raw, err := base64.RawURLEncoding.DecodeString(f[3])
	if err != nil {
		return err
	}
	var m restoreMetadata
	if err = json.Unmarshal(raw, &m); err != nil {
		return err
	}
	if !validRestoreMetadata(m) || !validRestoreStage(f[2], source, m.OperationID, m.MountID) {
		return errors.New("restore outside authenticated operation subtree")
	}
	dataset := f[2]
	unlock, err := lockBackupDataset(dataset)
	if err != nil {
		return err
	}
	defer unlock()
	if _, exists := run(ctx, "zfs", "list", "-H", "-o", "name", dataset); exists != nil {
		args := append([]string{"create", "-p"}, restoreProperties(source, m)...)
		if _, err = run(ctx, "zfs", append(args, dataset)...); err != nil {
			return err
		}
	}
	if err = verifyRestoreOwnership(ctx, dataset, source, m); err != nil {
		return err
	}
	switch f[1] {
	case "abort":
		_, err = run(ctx, "zfs", "receive", "-A", dataset)
		return err
	case "state":
		token, err := run(ctx, "zfs", "get", "-H", "-o", "value", "receive_resume_token", dataset)
		if err != nil {
			return err
		}
		if token == "-" {
			token = ""
		}
		listing, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-s", "createtxg", "-d", "1", dataset)
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(BackupState{Snapshots: parseBackupSnapshots(listing, dataset), ResumeToken: token})
	case "receive":
		// Even a full retry can roll back only this operation's isolated staging dataset.
		args := append([]string{"receive", "-u", "-s", "-F"}, restoreProperties(source, m)...)
		args = append(args, "-x", HoldTargetsProperty, "-x", TransferTargetsProperty, dataset)
		cmd := exec.CommandContext(ctx, "zfs", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, stderr
		return cmd.Run()
	}
	return errors.New("unreachable restore action")
}
