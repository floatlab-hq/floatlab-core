package lab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type RecoveryRequest struct {
	OperationID     string   `json:"operation_id"`
	SurvivorID      string   `json:"survivor_id"`
	ExcludedNodeIDs []string `json:"excluded_node_ids"`
	ConfirmFenced   bool     `json:"confirm_fenced"`
}
type RecoveryStatus struct {
	Request        RecoveryRequest `json:"request"`
	Phase          string          `json:"phase"`
	BackupPath     string          `json:"backup_path"`
	StartedAt      time.Time       `json:"started_at"`
	PendingNodeIDs []string        `json:"pending_node_ids,omitempty"`
}
type RecoveryConfig struct {
	LabDataDir, RQLiteDataDir, JournalDir string
	Stop                                  func(context.Context) error
	Start                                 func(context.Context) error
	Ready                                 func(context.Context) error
	DB                                    Database
	ApplyTrust                            func(context.Context, []Member) error
	FencePeers                            func(context.Context, bool) error
}

// RecoverLab is a root-only caller's explicit lost-quorum operation. It never
// touches local workload SQLite or starts any retained peer workloads.
func RecoverLab(ctx context.Context, cfg RecoveryConfig, req RecoveryRequest) (RecoveryStatus, error) {
	var status RecoveryStatus
	if !validID.MatchString(req.OperationID) || !validID.MatchString(req.SurvivorID) || !req.ConfirmFenced || len(req.ExcludedNodeIDs) == 0 {
		return status, errors.New("operation ID, survivor, excluded members and explicit fencing confirmation required")
	}
	if cfg.Stop == nil || cfg.Start == nil || cfg.Ready == nil || cfg.DB == nil || cfg.FencePeers == nil || cfg.ApplyTrust == nil {
		return status, errors.New("stopped-rqlite recovery runtime required")
	}
	unlock, err := update.Acquire(cfg.JournalDir)
	if err != nil {
		return status, err
	}
	defer unlock()
	stateData, err := os.ReadFile(filepath.Join(cfg.LabDataDir, "membership.json"))
	if err != nil {
		return status, err
	}
	var state State
	if err = json.Unmarshal(stateData, &state); err != nil {
		return status, err
	}
	if state.NodeID != req.SurvivorID {
		return status, errors.New("survivor must be this appliance's persisted identity")
	}
	excluded := map[string]bool{}
	for _, id := range req.ExcludedNodeIDs {
		if !validID.MatchString(id) || id == req.SurvivorID || excluded[id] {
			return status, errors.New("invalid excluded membership")
		}
		excluded[id] = true
	}
	var survivor Member
	for _, m := range state.Members {
		if m.NodeID == req.SurvivorID {
			survivor = m
			continue
		}
	}
	if survivor.NodeID == "" || survivor.Status != "active" {
		return status, errors.New("selected survivor must be an active cached member")
	}
	journalPath := filepath.Join(cfg.JournalDir, req.OperationID+".json")
	journals, err := filepath.Glob(filepath.Join(cfg.JournalDir, "*.json"))
	if err != nil {
		return status, err
	}
	for _, path := range journals {
		if path == journalPath {
			continue
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return status, e
		}
		var pending RecoveryStatus
		if e = json.Unmarshal(data, &pending); e != nil {
			return status, e
		}
		if pending.Phase != "complete" {
			return status, fmt.Errorf("resume unfinished recovery operation %s", pending.Request.OperationID)
		}
	}
	if data, readErr := os.ReadFile(journalPath); readErr == nil {
		if err = json.Unmarshal(data, &status); err != nil {
			return status, err
		}
		if status.Request.SurvivorID != req.SurvivorID {
			return status, errors.New("recovery survivor cannot change")
		}
		for _, id := range status.Request.ExcludedNodeIDs {
			if !excluded[id] {
				return status, errors.New("confirmed recovery exclusions cannot be withdrawn")
			}
		}
		if status.Phase == "complete" && len(req.ExcludedNodeIDs) != len(status.Request.ExcludedNodeIDs) {
			return status, errors.New("completed recovery exclusions cannot change")
		}
	} else if errors.Is(readErr, os.ErrNotExist) {
		for _, m := range state.Members {
			if m.NodeID != req.SurvivorID && m.Status != "removed" && !excluded[m.NodeID] {
				return status, fmt.Errorf("old member %s must be explicitly excluded and fenced", m.NodeID)
			}
		}
		for id := range excluded {
			found := false
			for _, m := range state.Members {
				found = found || m.NodeID == id
			}
			if !found {
				return status, fmt.Errorf("unknown excluded member %s", id)
			}
		}
		status = RecoveryStatus{Request: req, Phase: "confirmed", BackupPath: filepath.Join(cfg.JournalDir, "backup-"+req.OperationID), StartedAt: time.Now().UTC()}
		if err = update.WriteJSON(journalPath, status); err != nil {
			return status, err
		}
	} else {
		return status, readErr
	}
	for id := range excluded {
		found := false
		for _, m := range state.Members {
			found = found || m.NodeID == id
		}
		for _, pending := range status.PendingNodeIDs {
			found = found || pending == id
		}
		if !found {
			return status, fmt.Errorf("unknown excluded member %s", id)
		}
	}
	status.Request = req
	if err = update.WriteJSON(journalPath, status); err != nil {
		return status, err
	}
	checkpoint := func(phase string) error { status.Phase = phase; return update.WriteJSON(journalPath, status) }
	if status.Phase == "complete" {
		return status, nil
	}
	if err = cfg.FencePeers(ctx, true); err != nil {
		return status, err
	}
	if status.Phase == "confirmed" {
		if err = cfg.Stop(ctx); err != nil {
			return status, err
		}
		if err = BackupDirectory(cfg.RQLiteDataDir, filepath.Join(status.BackupPath, "rqlite")); err != nil {
			return status, err
		}
		if err = BackupDirectory(cfg.LabDataDir, filepath.Join(status.BackupPath, "lab")); err != nil {
			return status, err
		}
		if err = checkpoint("backed-up"); err != nil {
			return status, err
		}
	}
	if status.Phase == "backed-up" {
		if err = cfg.Stop(ctx); err != nil {
			return status, err
		}
		peers := []struct {
			ID       string `json:"id"`
			Address  string `json:"address"`
			NonVoter bool   `json:"non_voter"`
		}{{ID: req.SurvivorID, Address: survivor.RaftAddress, NonVoter: false}}
		if err = update.WriteJSON(filepath.Join(cfg.RQLiteDataDir, "raft", "peers.json"), peers); err != nil {
			return status, err
		}
		if err = checkpoint("membership-written"); err != nil {
			return status, err
		}
	}
	if status.Phase == "membership-written" {
		if err = cfg.Start(ctx); err != nil {
			return status, err
		}
		if err = cfg.Ready(ctx); err != nil {
			return status, err
		}
		if err = checkpoint("ready"); err != nil {
			return status, err
		}
	}
	if status.Phase == "ready" {
		rows, readErr := cfg.DB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members ORDER BY node_id`})
		if readErr != nil {
			return status, readErr
		}
		members := map[string]Member{}
		for _, m := range state.Members {
			members[m.NodeID] = m
		}
		foundSurvivor := false
		for _, row := range rows.Values {
			if len(row) != 1 {
				return status, errors.New("invalid recovered lab member")
			}
			var m Member
			if err = json.Unmarshal([]byte(fmt.Sprint(row[0])), &m); err != nil || !validID.MatchString(m.NodeID) {
				return status, errors.New("invalid recovered lab member identity")
			}
			if m.NodeID == req.SurvivorID {
				if m.Status != "active" {
					return status, errors.New("recovered configuration does not authorize the selected survivor")
				}
				foundSurvivor = true
			}
			// A tombstone learned before recovery must never be resurrected.
			if old, exists := members[m.NodeID]; exists && old.Status == "removed" {
				m = old
			}
			members[m.NodeID] = m
		}
		if !foundSurvivor {
			return status, errors.New("selected survivor is absent from recovered configuration")
		}
		status.PendingNodeIDs = nil
		for _, m := range members {
			if m.NodeID != req.SurvivorID && m.Status != "removed" && !excluded[m.NodeID] {
				status.PendingNodeIDs = append(status.PendingNodeIDs, m.NodeID)
			}
		}
		if len(status.PendingNodeIDs) > 0 {
			if err = update.WriteJSON(journalPath, status); err != nil {
				return status, err
			}
			return status, fmt.Errorf("recovered members require explicit fencing confirmation; resume this operation with expanded exclusions: %s", strings.Join(status.PendingNodeIDs, ", "))
		}
		state.Members = nil
		for _, m := range members {
			state.Members = append(state.Members, m)
		}
		stmts := make([]rqlite.Statement, 0, len(excluded))
		for i := range state.Members {
			m := &state.Members[i]
			if excluded[m.NodeID] {
				m.Status = "removed"
				m.OperationID = req.OperationID
				m.RemovedAt = time.Now().UTC().Format(time.RFC3339Nano)
			}
			body, _ := json.Marshal(m)
			stmts = append(stmts, rqlite.Statement{SQL: `INSERT INTO lab_members(node_id,body) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET body=excluded.body`, Params: []interface{}{m.NodeID, string(body)}})
		}
		stmts = append(stmts, rqlite.Statement{SQL: `UPDATE lab_trust_meta SET revision=revision+1 WHERE id=1`})
		if err = cfg.DB.Execute(ctx, stmts); err != nil {
			return status, err
		}
		if err = cfg.ApplyTrust(ctx, state.Members); err != nil {
			return status, err
		}
		if err = update.WriteJSON(filepath.Join(cfg.LabDataDir, "membership.json"), state); err != nil {
			return status, err
		}
		if err = checkpoint("trust-applied"); err != nil {
			return status, err
		}
	}
	if status.Phase == "trust-applied" {
		if err = cfg.FencePeers(ctx, false); err != nil {
			return status, err
		}
		if err = checkpoint("complete"); err != nil {
			return status, err
		}
	}
	return status, nil
}

// BackupDirectory copies a stopped service's entire private directory, rejects
// symlinks and special files, and re-reads every copied file to verify its hash.
// A manifest is written only after verification. Existing backups are verified
// rather than overwritten, keeping interrupted recovery safely resumable.
func BackupDirectory(source, destination string) error {
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	if source == destination || strings.HasPrefix(destination, source+string(os.PathSeparator)) {
		return errors.New("backup must be outside source directory")
	}
	manifestPath := destination + ".manifest.json"
	if data, readErr := os.ReadFile(manifestPath); readErr == nil {
		var hashes map[string]string
		if err = json.Unmarshal(data, &hashes); err != nil {
			return err
		}
		for name, want := range hashes {
			if name == "." || filepath.IsAbs(name) || strings.HasPrefix(filepath.Clean(name), "..") {
				return errors.New("invalid backup manifest path")
			}
			got, e := hashFile(filepath.Join(destination, name))
			if e != nil {
				return e
			}
			if got != want {
				return fmt.Errorf("backup checksum mismatch: %s", name)
			}
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	hashes := map[string]string{}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, e := filepath.Rel(source, path)
		if e != nil {
			return e
		}
		target := filepath.Join(destination, relative)
		info, e := entry.Info()
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing non-regular backup source: %s", relative)
		}
		input, e := os.Open(path)
		if e != nil {
			return e
		}
		defer input.Close()
		output, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if e != nil {
			return e
		}
		hash := sha256.New()
		_, e = io.Copy(io.MultiWriter(output, hash), input)
		if e == nil {
			e = output.Sync()
		}
		closeErr := output.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
		want := hex.EncodeToString(hash.Sum(nil))
		got, e := hashFile(target)
		if e != nil {
			return e
		}
		if got != want {
			return fmt.Errorf("backup verification failed: %s", relative)
		}
		hashes[relative] = want
		return nil
	})
	if err != nil {
		return err
	}
	return update.WriteJSON(manifestPath, hashes)
}
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
