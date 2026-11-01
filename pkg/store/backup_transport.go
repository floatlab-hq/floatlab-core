package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type commandBackup struct{ r ReplicationRequest }

type backupDiagnostics struct{ bytes.Buffer }

func (b *backupDiagnostics) Write(p []byte) (int, error) {
	n := len(p)
	if remain := (64 << 10) - b.Len(); remain > 0 {
		if len(p) > remain {
			p = p[:remain]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}

func BackupDataset(source, dataset string) string {
	return strings.Split(dataset, "/")[0] + "/backups/" + source + "/" + dataset
}
func Replicate(ctx context.Context, r ReplicationRequest) (*SendResult, error) {
	if !validBackupDataset(r.Dataset) || !backupID.MatchString(r.SourceNodeID) || !backupID.MatchString(r.DestNodeID) || r.Generation < 0 {
		return nil, errors.New("invalid source dataset")
	}
	ctx, finish := registerBackup(ctx, r.Dataset+"@"+r.DestNodeID, r.Generation)
	defer finish()
	unlock, err := lockBackupDataset(r.Dataset)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	removed, err := removedBackupTargets(ctx, r.Dataset)
	if err != nil {
		return nil, err
	}
	if cutoff, found := removed[r.DestNodeID]; found && r.Generation <= cutoff {
		return nil, errors.New("backup target was removed; refusing stale replication generation")
	}
	if r.DestDataset == "" {
		r.DestDataset = BackupDataset(r.SourceNodeID, r.Dataset)
	}
	if !validBackupDataset(r.DestDataset) || !strings.Contains(r.DestDataset, "/backups/"+r.SourceNodeID+"/") {
		return nil, errors.New("invalid owned backup destination")
	}
	if r.DestPort == 0 {
		r.DestPort = 22
	}
	if r.DestPort < 1 || r.DestPort > 65535 {
		return nil, errors.New("invalid SSH port")
	}
	host := strings.Trim(r.DestHost, "[]")
	if net.ParseIP(host) == nil {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]*$`).MatchString(host) {
			return nil, errors.New("invalid SSH host")
		}
	}
	r.DestHost = host
	if r.IdentityFile == "" {
		r.IdentityFile = "/floatlab/system/workloads/lab/replication-key"
	}
	if r.KnownHostsFile == "" {
		r.KnownHostsFile = "/floatlab/system/workloads/lab/known_hosts"
	}
	if err = acceptBackupGenerations(ctx, r.Dataset, map[string]int64{r.DestNodeID: r.Generation}); err != nil {
		return nil, err
	}
	return ReplicateBackup(ctx, r, &commandBackup{r})
}

func lockBackupDataset(dataset string) (func(), error) {
	if err := os.MkdirAll("/run/floatlab/backup-locks", 0700); err != nil {
		return nil, err
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(dataset)))
	file, err := os.OpenFile("/run/floatlab/backup-locks/"+key, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}
func (b *commandBackup) remoteArgs(action string) []string {
	policy := base64.RawURLEncoding.EncodeToString([]byte(b.r.Policy))
	if policy == "" {
		policy = "-"
	}
	return []string{"-T", "-o", "StrictHostKeyChecking=yes", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "HostKeyAlias=" + b.r.DestNodeID, "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", "-o", "UserKnownHostsFile=" + b.r.KnownHostsFile, "-i", b.r.IdentityFile, "-p", strconv.Itoa(b.r.DestPort), "floatlab-replica@" + b.r.DestHost, "zfs-backup", action, b.r.DestDataset, policy}
}
func (b *commandBackup) Source(ctx context.Context, dataset string) ([]BackupSnapshot, error) {
	out, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,guid", "-s", "createtxg", "-d", "1", dataset)
	if err != nil {
		return nil, err
	}
	all := parseBackupSnapshots(out, dataset)
	snaps := []BackupSnapshot{}
	for _, s := range all {
		if IsAutomatedSnapshot(s.Name) {
			snaps = append(snaps, s)
		}
	}
	return snaps, nil
}
func parseBackupSnapshots(out, dataset string) []BackupSnapshot {
	var snaps []BackupSnapshot
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.HasPrefix(f[0], dataset+"@") {
			name := strings.TrimPrefix(f[0], dataset+"@")
			snaps = append(snaps, BackupSnapshot{Name: name, GUID: f[1]})
		}
	}
	return snaps
}
func (b *commandBackup) State(ctx context.Context) (BackupState, error) {
	out, err := run(ctx, "ssh", b.remoteArgs("state")...)
	var state BackupState
	if err == nil {
		err = json.Unmarshal([]byte(out), &state)
	}
	return state, err
}
func (b *commandBackup) Abort(ctx context.Context) error {
	_, err := run(ctx, "ssh", b.remoteArgs("abort")...)
	return err
}
func (b *commandBackup) Prune(ctx context.Context) error {
	_, err := run(ctx, "ssh", b.remoteArgs("prune")...)
	return err
}
func (b *commandBackup) Protect(ctx context.Context, snapshot, node, kind string, add bool) error {
	return UpdateSnapshotProtection(ctx, b.r.Dataset, snapshot, node, kind, add)
}
func (b *commandBackup) ValidateToken(ctx context.Context, token string) error {
	return b.validateToken(ctx, token, "")
}

func (b *commandBackup) validateToken(ctx context.Context, token, snapshot string) error {
	if token == "" || strings.ContainsAny(token, "\r\n\x00") {
		return ErrInvalidResumeToken
	}
	output, err := exec.CommandContext(ctx, "zfs", "send", "-nvt", token).CombinedOutput()
	out := string(output)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.Contains(out, "resume token is invalid") || strings.Contains(out, "resume token is corrupt") || strings.Contains(out, "invalid resume token") || strings.Contains(out, "no longer exists") || strings.Contains(out, "does not exist") {
			return fmt.Errorf("%w: %v", ErrInvalidResumeToken, err)
		}
		return err
	}
	// OpenZFS dry-run token output includes the fully qualified original target.
	name, guid := "", ""
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 3 && f[1] == "=" {
			if f[0] == "toname" {
				name = f[2]
			}
			if f[0] == "toguid" {
				v, e := strconv.ParseUint(f[2], 0, 64)
				if e == nil {
					guid = strconv.FormatUint(v, 10)
				}
			}
		}
	}
	parts := strings.Split(name, "@")
	if len(parts) != 2 || parts[0] != b.r.Dataset || !IsAutomatedSnapshot(parts[1]) || guid == "" {
		return ErrInvalidResumeToken
	}
	if snapshot != "" && parts[1] != snapshot {
		return ErrInvalidResumeToken
	}
	snaps, err := b.Source(ctx, b.r.Dataset)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.Name == parts[1] && s.GUID == guid {
			return nil
		}
	}
	return ErrInvalidResumeToken
}
func (b *commandBackup) Send(ctx context.Context, args []string) (int64, error) {
	return sendBackupStream(ctx, args, b.remoteArgs("receive"))
}

func sendBackupStream(ctx context.Context, args, remote []string) (int64, error) {
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	send := exec.CommandContext(child, "zfs", args...)
	recv := exec.CommandContext(child, "ssh", remote...)
	var se, re backupDiagnostics
	send.Stderr = &se
	recv.Stderr = &re
	reader, err := send.StdoutPipe()
	if err != nil {
		return 0, err
	}
	writer, err := recv.StdinPipe()
	if err != nil {
		return 0, err
	}
	if err = recv.Start(); err != nil {
		return 0, err
	}
	if err = send.Start(); err != nil {
		writer.Close()
		cancel()
		_ = recv.Wait()
		return 0, err
	}
	done := make(chan error, 1)
	go func() {
		err := recv.Wait()
		if err != nil {
			cancel()
		}
		done <- err
	}()
	n, copyErr := io.Copy(writer, reader)
	_ = writer.Close()
	sendErr := send.Wait()
	recvErr := <-done
	if err = errors.Join(copyErr, sendErr, recvErr); err != nil {
		return n, fmt.Errorf("replication stream: %w; send: %s; receive: %s", err, se.String(), re.String())
	}
	return n, nil
}

// ServeBackupReceiver accepts the forced SSH source identity.
func ServeBackupReceiver(ctx context.Context, source, command string, in io.Reader, out, stderr io.Writer) error {
	fields := strings.Fields(command)
	if len(fields) > 0 && fields[0] == "zfs-restore" {
		return ServeRestoreReceiver(ctx, source, command, in, out, stderr)
	}
	if !backupID.MatchString(source) || len(fields) != 4 || fields[0] != "zfs-backup" {
		return errors.New("invalid backup command")
	}
	action, dataset := fields[1], fields[2]
	parts := strings.Split(dataset, "/")
	if !validBackupDataset(dataset) || len(parts) < 5 || parts[1] != "backups" || parts[2] != source {
		return errors.New("backup destination outside authenticated source subtree")
	}
	if action != "state" && action != "receive" && action != "abort" && action != "prune" {
		return errors.New("invalid backup action")
	}
	policy := ""
	if fields[3] != "-" {
		raw, err := base64.RawURLEncoding.DecodeString(fields[3])
		if err != nil {
			return err
		}
		policy = string(raw)
		if _, err = ParseSnapshotPolicy(policy); err != nil {
			return err
		}
	}
	unlock, err := lockBackupDataset(dataset)
	if err != nil {
		return err
	}
	defer unlock()
	_, exists := run(ctx, "zfs", "list", "-H", "-o", "name", dataset)
	if exists != nil {
		// A list failure can be permission or pool failure: creation must succeed before proceeding.
		if _, err = run(ctx, "zfs", "create", "-p", "-o", "mountpoint=none", "-o", "readonly=on", "-o", "floatlab:backup-source="+source, dataset); err != nil {
			return err
		}
	}
	owner, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", "floatlab:backup-source", dataset)
	if err != nil {
		return err
	}
	if owner != source {
		return errors.New("refusing unowned backup dataset")
	}
	switch action {
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
		snaps := parseBackupSnapshots(listing, dataset)
		return json.NewEncoder(out).Encode(BackupState{Snapshots: snaps, ResumeToken: token})
	case "abort":
		_, err = run(ctx, "zfs", "receive", "-A", dataset)
		return err
	case "prune":
		if policy == "" {
			return nil
		}
		if _, err = run(ctx, "zfs", "set", "floatlab:backup-policy="+policy, dataset); err != nil {
			return err
		}
		return pruneSnapshots(ctx, dataset, policy)
	case "receive":
		cmd := exec.CommandContext(ctx, "zfs", "receive", "-u", "-s", "-F", "-o", "readonly=on", "-o", "mountpoint=none", "-o", "floatlab:backup-source="+source, "-x", HoldTargetsProperty, "-x", TransferTargetsProperty, dataset)
		cmd.Stdin = in
		cmd.Stdout = out
		cmd.Stderr = stderr
		return cmd.Run()
	}
	return errors.New("unreachable backup action")
}
