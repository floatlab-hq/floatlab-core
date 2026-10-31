package rqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// SQLiteRsync uses SQLite's native origin/replica protocol directly over SSH.
// The SSH key must force the destination's ServeReplicaCommand wrapper.
type SQLiteRsync struct{ Executable, Version string }

const PinnedSQLiteRsyncVersion = "3.50.4"
const pinnedSQLiteSourceID = "2025-07-30 19:33:53 4d8adfb30e03f9cf27f800a2c1ba3c48fb4ca1b08b0f5ed59a4d5ecbf45e20a3"

func (s SQLiteRsync) check(ctx context.Context) error {
	if s.Executable == "" || s.Version == "" {
		return errors.New("pinned sqlite3_rsync executable and version required")
	}
	out, err := exec.CommandContext(ctx, s.Executable, "--version").Output()
	if err != nil {
		return err
	}
	// This utility prints SQLITE_SOURCE_ID, not the semantic SQLite version.
	if s.Version != PinnedSQLiteRsyncVersion || strings.TrimSpace(string(out)) != pinnedSQLiteSourceID {
		return fmt.Errorf("sqlite3_rsync version mismatch: require %s", s.Version)
	}
	return nil
}

func sshArgs(p ReplicaPeer) ([]string, error) {
	if !validNodeID(p.NodeID) || !regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`).MatchString(p.SSHUser) || p.IdentityFile == "" || p.KnownHostsFile == "" {
		return nil, errors.New("invalid restricted SSH peer configuration")
	}
	host, port, err := net.SplitHostPort(p.Address)
	if err != nil {
		host = p.Address
		port = "22"
	}
	if host == "" || strings.ContainsAny(host, " \t\r\n/\\@") || strings.HasPrefix(host, "-") {
		return nil, errors.New("invalid replica SSH host")
	}
	for _, r := range port {
		if r < '0' || r > '9' {
			return nil, errors.New("invalid replica SSH port")
		}
	}
	return []string{"-T", "-o", "BatchMode=yes", "-o", "IdentitiesOnly=yes", "-o", "StrictHostKeyChecking=yes", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "UserKnownHostsFile=" + p.KnownHostsFile, "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-i", p.IdentityFile, "-p", port, p.SSHUser + "@" + host}, nil
}

func (s SQLiteRsync) Transfer(ctx context.Context, p ReplicaPeer, path string) (LocalIdentity, error) {
	var id LocalIdentity
	if err := s.check(ctx); err != nil {
		return id, err
	}
	args, err := sshArgs(p)
	if err != nil {
		return id, err
	}
	// Bidirectional pipes avoid shell quoting or caller-selected remote paths.
	origin := exec.CommandContext(ctx, s.Executable, "--origin", path, "replica")
	remote := exec.CommandContext(ctx, "ssh", append(args, "replicate")...)
	a, err := origin.StdoutPipe()
	if err != nil {
		return id, err
	}
	b, err := remote.StdoutPipe()
	if err != nil {
		return id, err
	}
	remote.Stdin = a
	origin.Stdin = b
	var originDiagnostics, remoteDiagnostics bytes.Buffer
	origin.Stderr = &originDiagnostics
	remote.Stderr = &remoteDiagnostics
	if err = remote.Start(); err != nil {
		return id, err
	}
	if err = origin.Start(); err != nil {
		_ = remote.Process.Kill()
		_ = remote.Wait()
		return id, err
	}
	localErr := origin.Wait()
	if localErr != nil {
		_ = remote.Process.Kill()
	}
	remoteErr := remote.Wait()
	if localErr != nil || remoteErr != nil {
		return id, fmt.Errorf("sqlite3_rsync failed: origin %v, peer %v: %s %s", localErr, remoteErr, originDiagnostics.String(), remoteDiagnostics.String())
	}
	out, err := exec.CommandContext(ctx, "ssh", append(args, "status")...).Output()
	if err != nil {
		return id, err
	}
	if err = json.Unmarshal(out, &id); err != nil {
		return id, err
	}
	return id, nil
}

type ReplicaReceiver struct {
	Root         string
	SourceNodeID string
	SQLiteRsync
}

// ServeReplicaCommand is called only for an enrolled, forced-command SSH key.
// SourceNodeID is bound to that key, never taken from SSH_ORIGINAL_COMMAND.
func ServeReplicaCommand(ctx context.Context, r ReplicaReceiver, command string, stdin io.Reader, stdout, stderr io.Writer) error {
	if !validNodeID(r.SourceNodeID) {
		return errors.New("invalid replica source")
	}
	if command != "replicate" && command != "status" {
		return errors.New("replication SSH command denied")
	}
	if err := os.MkdirAll(r.Root, 0700); err != nil {
		return err
	}
	published := filepath.Join(r.Root, r.SourceNodeID+".sqlite")
	if command == "status" {
		id, err := ReadReplicaIdentity(ctx, published)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(id)
	}
	if err := r.check(ctx); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(r.Root, r.SourceNodeID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("replica receive already active")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	staging := filepath.Join(r.Root, "."+r.SourceNodeID+".staging.sqlite")
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if err = os.Remove(staging + suffix); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if _, err = os.Stat(published); err == nil {
		db, e := openReplica(published)
		if e != nil {
			return e
		}
		_, e = db.ExecContext(ctx, `VACUUM INTO ?`, staging)
		db.Close()
		if e != nil {
			return e
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	child := exec.CommandContext(ctx, r.Executable, "--replica", "origin", staging)
	child.Stdin = stdin
	child.Stdout = stdout
	child.Stderr = stderr
	if err = child.Run(); err != nil {
		return err
	}
	return PublishReplica(ctx, staging, published, r.SourceNodeID)
}

func openReplica(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro&_pragma=busy_timeout(10000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func ReadReplicaIdentity(ctx context.Context, path string) (LocalIdentity, error) {
	var id LocalIdentity
	db, err := openReplica(path)
	if err != nil {
		return id, err
	}
	defer db.Close()
	var schema int
	if err = db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&schema); err != nil {
		return id, err
	}
	if schema != LocalSchemaVersion {
		return id, fmt.Errorf("unsupported replica schema %d", schema)
	}
	err = db.QueryRowContext(ctx, `SELECT node_id,generation,revision FROM workload_identity WHERE singleton=1`).Scan(&id.NodeID, &id.Generation, &id.Revision)
	if err == nil && (!validNodeID(id.NodeID) || id.Generation == "" || id.Revision < 0) {
		err = errors.New("invalid replica identity")
	}
	return id, err
}

// OpenReplica opens a peer view without migration or write permissions.
func OpenReplica(path string) (*Client, error) {
	db, err := openReplica(path)
	if err != nil {
		return nil, err
	}
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return &Client{local: &localDB{db: db, path: path}}, nil
}

// PublishReplica validates the received source and monotonic revision before
// replacing the previous standalone read-only file. Generations never auto-change.
func PublishReplica(ctx context.Context, staging, published, source string) error {
	if !validNodeID(source) {
		return errors.New("invalid source identity")
	}
	id, err := ReadReplicaIdentity(ctx, staging)
	if err != nil {
		return err
	}
	if id.NodeID != source {
		return errors.New("replica source identity mismatch")
	}
	if old, e := ReadReplicaIdentity(ctx, published); e == nil {
		if old.Generation != id.Generation || id.Revision < old.Revision {
			return errors.New("replica generation changed or revision regressed")
		}
	} else if _, statErr := os.Stat(published); !os.IsNotExist(statErr) {
		return fmt.Errorf("cannot validate existing replica: %w", e)
	}
	db, err := sql.Open("sqlite", staging)
	if err != nil {
		return err
	}
	var integrity string
	if err = db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err == nil && integrity != "ok" {
		err = fmt.Errorf("replica integrity: %s", integrity)
	}
	if err == nil {
		_, err = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE); PRAGMA journal_mode=DELETE`)
	}
	if e := db.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	f, err := os.OpenFile(staging, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	if err = os.Chmod(staging, 0400); err != nil {
		return err
	}
	if err = os.Rename(staging, published); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(published))
}
