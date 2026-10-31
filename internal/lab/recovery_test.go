package lab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func recoveryFixture(t *testing.T) (*Service, *rqlite.Client, RecoveryConfig, RecoveryRequest) {
	t.Helper()
	s, db := testLab(t)
	ctx := context.Background()
	if err := s.putMember(ctx, Member{NodeID: "second", Status: "active", PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshTrust(ctx); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(t.TempDir(), "rqlite")
	if err := os.MkdirAll(filepath.Join(data, "raft"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "raft", "raft.db"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := RecoveryConfig{LabDataDir: s.cfg.DataDir, RQLiteDataDir: data, JournalDir: filepath.Join(t.TempDir(), "recovery"), DB: db, Stop: func(context.Context) error { return nil }, Start: func(context.Context) error { return nil }, Ready: func(context.Context) error { return nil }, ApplyTrust: func(context.Context, []Member) error { return nil }, FencePeers: func(context.Context, bool) error { return nil }}
	req := RecoveryRequest{OperationID: "recovery", SurvivorID: "owner", ExcludedNodeIDs: []string{"second"}, ConfirmFenced: true}
	return s, db, cfg, req
}

func TestRecoveryKeepsQuarantineAcrossInterruptedCheckpoints(t *testing.T) {
	for _, checkpoint := range []string{"stop", "start", "ready", "trust", "unfence"} {
		t.Run(checkpoint, func(t *testing.T) {
			_, _, cfg, req := recoveryFixture(t)
			fenced, injected := false, false
			fail := func(at string) error {
				if at == checkpoint && !injected {
					injected = true
					return errors.New("injected " + at)
				}
				return nil
			}
			cfg.FencePeers = func(_ context.Context, enabled bool) error {
				if !enabled {
					if err := fail("unfence"); err != nil {
						return err
					}
				}
				fenced = enabled
				return nil
			}
			cfg.Stop = func(context.Context) error {
				if !fenced {
					t.Fatal("rqlite changed before peer quarantine")
				}
				return fail("stop")
			}
			cfg.Start = func(context.Context) error {
				if !fenced {
					t.Fatal("rqlite started without peer quarantine")
				}
				return fail("start")
			}
			cfg.Ready = func(context.Context) error { return fail("ready") }
			cfg.ApplyTrust = func(context.Context, []Member) error { return fail("trust") }
			if _, err := RecoverLab(context.Background(), cfg, req); err == nil || !fenced {
				t.Fatal("interrupted recovery exposed peers", err, fenced)
			}
			status, err := RecoverLab(context.Background(), cfg, req)
			if err != nil || status.Phase != "complete" || fenced {
				t.Fatal("recovery did not resume safely", status, err, fenced)
			}
			backup, err := os.ReadFile(filepath.Join(status.BackupPath, "rqlite", "raft", "raft.db"))
			if err != nil || string(backup) != "original" {
				t.Fatal("original recovery backup lost", err)
			}
		})
	}
}

func TestRecoveryRequiresFencingNewlyRecoveredMembersAndPreservesTombstones(t *testing.T) {
	s, db, cfg, req := recoveryFixture(t)
	ctx := context.Background()
	third := Member{NodeID: "third", Status: "active", PeerURL: "https://127.0.0.3:9443", RaftAddress: "127.0.0.3:4002"}
	if err := s.putMember(ctx, third); err != nil {
		t.Fatal(err)
	}
	old := Member{NodeID: "old", Status: "removed", PeerURL: "https://127.0.0.4:9443", RaftAddress: "127.0.0.4:4002"}
	s.state.Members = append(s.state.Members, old)
	if err := s.saveState(); err != nil {
		t.Fatal(err)
	}
	old.Status = "active"
	if err := s.putMember(ctx, old); err != nil {
		t.Fatal(err)
	}
	fenced, applied := false, false
	cfg.FencePeers = func(_ context.Context, enabled bool) error { fenced = enabled; return nil }
	cfg.ApplyTrust = func(_ context.Context, members []Member) error {
		applied = true
		for _, m := range members {
			if m.NodeID != "owner" && m.Status != "removed" {
				t.Fatal("unrevoked recovered member", m)
			}
		}
		return nil
	}
	status, err := RecoverLab(ctx, cfg, req)
	if err == nil || !strings.Contains(err.Error(), "third") || !fenced || applied || status.Phase != "ready" {
		t.Fatal("unknown recovered member did not retain quarantine", status, err, fenced, applied)
	}
	withdrawn := req
	withdrawn.ExcludedNodeIDs = []string{"third"}
	if _, err = RecoverLab(ctx, cfg, withdrawn); err == nil {
		t.Fatal("withdrew a previously confirmed exclusion")
	}
	req.ExcludedNodeIDs = append(req.ExcludedNodeIDs, "third")
	status, err = RecoverLab(ctx, cfg, req)
	if err != nil || status.Phase != "complete" || fenced || !applied {
		t.Fatal("expanded fencing confirmation did not resume", status, err)
	}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id<>'owner'`})
	if err != nil || len(rows.Values) != 3 {
		t.Fatal(rows, err)
	}
	for _, row := range rows.Values {
		var m Member
		if err := json.Unmarshal([]byte(row[0].(string)), &m); err != nil || m.Status != "removed" {
			t.Fatal("recovered identity escaped tombstone", m, err)
		}
	}
}
