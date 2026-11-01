package lab

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func testLab(t *testing.T) (*Service, *rqlite.Client) {
	t.Helper()
	dir := t.TempDir()
	db, err := rqlite.OpenLocal(filepath.Join(dir, "lab.sqlite"), "owner")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(context.Background(), Config{NodeID: "owner", PeerURL: "https://127.0.0.1:9443", RaftAddress: "127.0.0.1:4002", RQLiteAddress: "127.0.0.1:4001", DataDir: filepath.Join(dir, "identity"), DB: db})
	if err != nil {
		t.Fatal(err)
	}
	return s, db
}

func TestEnrollmentInvitationBoundToOneKeyAndRevocable(t *testing.T) {
	s, db := testLab(t)
	ctx := context.Background()
	inv, err := s.CreateInvitation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, csr, err := keyAndCSR("second")
	if err != nil {
		t.Fatal(err)
	}
	req := enrollmentRequest{Token: inv.Token, NodeID: "second", CSR: string(csr), PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001"}
	first, err := s.enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.CertificatePEM != second.CertificatePEM {
		t.Fatal("retry minted another certificate")
	}
	_, differentCSR, _ := keyAndCSR("second")
	changed := req
	changed.CSR = string(differentCSR)
	if _, err = s.enroll(ctx, changed); err == nil {
		t.Fatal("accepted second key for single-use invitation")
	}
	changed = req
	changed.NodeID = "third"
	if _, err = s.enroll(ctx, changed); err == nil {
		t.Fatal("accepted second identity for single-use invitation")
	}
	var m Member
	for _, member := range s.Snapshot().Members {
		if member.NodeID == "second" {
			m = member
		}
	}
	m.Status = "active"
	if err = s.putMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshTrust(ctx); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(first.CertificatePEM))
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckPeer(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err != nil {
		t.Fatal(err)
	}
	m.Status = "removed"
	if err = s.putMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err = s.RefreshTrust(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckPeer(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}); err == nil {
		t.Fatal("removed cert remained authorized")
	}
	if _, err = s.enroll(ctx, req); err == nil {
		t.Fatal("invitation retry resurrected removed identity")
	}
	r, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id='second'`})
	if err != nil || !strings.Contains(r.Values[0][0].(string), "removed") {
		t.Fatal("tombstone lost", err)
	}
}

func TestExpiredClaimedInvitationRequiresFreshPrivateKeyProof(t *testing.T) {
	s, db := testLab(t)
	ctx := context.Background()
	inv, err := s.CreateInvitation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	key, csr, err := keyAndCSR("second")
	if err != nil {
		t.Fatal(err)
	}
	req := enrollmentRequest{Token: inv.Token, NodeID: "second", CSR: string(csr), PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001"}
	first, err := s.enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	expire := func(token string) {
		t.Helper()
		sum := sha256.Sum256([]byte(token))
		if err := db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE lab_invitations SET expires_at=? WHERE token_hash=?`, Params: []any{time.Now().Add(-time.Minute).Format(time.RFC3339Nano), fmt.Sprintf("%x", sum)}}}); err != nil {
			t.Fatal(err)
		}
	}
	expire(inv.Token)
	if _, err := s.enroll(ctx, req); err == nil {
		t.Fatal("copied token and public CSR bypassed invitation expiry")
	}
	if err := signEnrollmentRetry(&req, key, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if replay, err := s.enroll(ctx, req); err != nil || replay.CertificatePEM != first.CertificatePEM {
		t.Fatal("fresh private-key proof could not resume claimed enrollment", err)
	}
	wrongKey, _, err := keyAndCSR("second")
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct {
		name      string
		key       []byte
		timestamp int64
	}{{"wrong key", wrongKey, time.Now().Unix()}, {"stale proof", key, time.Now().Add(-6 * time.Minute).Unix()}, {"future proof", key, time.Now().Add(6 * time.Minute).Unix()}} {
		t.Run(invalid.name, func(t *testing.T) {
			bad := req
			if err := signEnrollmentRetry(&bad, invalid.key, invalid.timestamp); err != nil {
				t.Fatal(err)
			}
			if _, err := s.enroll(ctx, bad); err == nil {
				t.Fatal("accepted invalid expired-invitation proof")
			}
		})
	}
	unclaimed, err := s.CreateInvitation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expire(unclaimed.Token)
	req.Token = unclaimed.Token
	if err := signEnrollmentRetry(&req, key, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.enroll(ctx, req); err == nil {
		t.Fatal("private-key proof revived an expired unclaimed invitation")
	}
}

func TestTLSBootstrapPinsRootAndChecksHostname(t *testing.T) {
	s, _ := testLab(t)
	config, err := s.BootstrapTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(s.EnrollmentHandler())
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	inv, err := s.CreateInvitation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inv.BootstrapURL = server.URL
	_, csr, _ := keyAndCSR("second")
	req := enrollmentRequest{Token: inv.Token, NodeID: "second", CSR: string(csr), PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001"}
	if _, err = requestEnrollment(context.Background(), inv, req); err != nil {
		t.Fatal(err)
	}
	inv.Fingerprint = strings.Repeat("0", 64)
	if _, err = requestEnrollment(context.Background(), inv, req); err == nil {
		t.Fatal("accepted wrong CA pin")
	}
}

func TestJoinedNodeCanIssueInvitationsWithoutOriginalAuthority(t *testing.T) {
	source, db := testLab(t)
	ctx := context.Background()
	tlsConfig, err := source.BootstrapTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(source.EnrollmentHandler())
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()
	inv, err := source.CreateInvitation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inv.BootstrapURL = server.URL
	dir := t.TempDir()
	ownDB, err := rqlite.OpenLocal(filepath.Join(dir, "own.sqlite"), "joining")
	if err != nil {
		t.Fatal(err)
	}
	defer ownDB.Close()
	var joining *Service
	remover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/remove" {
			t.Errorf("unexpected removal request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer remover.Close()
	joining, err = Open(ctx, Config{NodeID: "joining", DB: ownDB, RQLiteURL: remover.URL, DataDir: filepath.Join(dir, "lab"), PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001", RequireEmpty: func(context.Context) error { return nil }, ApplyJoin: func(context.Context, JoinBundle) error { joining.cfg.DB = db; return nil }, Promote: func(context.Context, JoinBundle) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	request := JoinRequest{Invitation: inv, PeerURL: joining.cfg.PeerURL, RaftAddress: joining.cfg.RaftAddress, RQLiteAddress: joining.cfg.RQLiteAddress}
	if _, err = joining.Join(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err = joining.CreateInvitation(ctx); err != nil {
		t.Fatal("joined node lost enrollment authority", err)
	}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT response FROM lab_invitations WHERE claimed_by='joining'`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rows.Values[0][0].(string), "PRIVATE KEY") {
		t.Fatal("CA private key persisted in invitation table")
	}
	data, err := os.ReadFile(filepath.Join(joining.cfg.DataDir, "join.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("completed join journal retained signing or leaf key")
	}
}

type laggingLabDatabase struct{ Database }

func (db laggingLabDatabase) QueryLocal(context.Context, rqlite.Statement) (*rqlite.QueryResult, error) {
	return &rqlite.QueryResult{}, nil
}

func TestEnrollmentResumesIdentitySwitchAndPromotionAfterRestart(t *testing.T) {
	for _, failure := range []string{"credentials", "catchup", "promotion", "config"} {
		t.Run(failure, func(t *testing.T) {
			source, shared := testLab(t)
			ctx := context.Background()
			tlsConfig, err := source.BootstrapTLSConfig()
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewUnstartedServer(source.EnrollmentHandler())
			server.TLS = tlsConfig
			server.StartTLS()
			defer server.Close()
			inv, err := source.CreateInvitation(ctx)
			if err != nil {
				t.Fatal(err)
			}
			inv.BootstrapURL = server.URL
			dir := t.TempDir()
			standalone, err := rqlite.OpenLocal(filepath.Join(dir, "standalone.sqlite"), "joining")
			if err != nil {
				t.Fatal(err)
			}
			defer standalone.Close()
			removals, promotions, configurations := 0, 0, 0
			stagedTrust := false
			configSyncs := 0
			remover := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodDelete || r.URL.Path != "/remove" {
					t.Errorf("unexpected membership request %s %s", r.Method, r.URL.Path)
				}
				removals++
				w.WriteHeader(http.StatusOK)
			}))
			defer remover.Close()
			var joining *Service
			cfg := Config{NodeID: "joining", DB: standalone, RQLiteURL: remover.URL, DataDir: filepath.Join(dir, "lab"), PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001", RequireEmpty: func(context.Context) error { return nil }, ApplyJoin: func(context.Context, JoinBundle) error {
				if !stagedTrust || !joining.JoinPending() {
					t.Fatal("rqlite join started before authenticated peer trust was staged")
				}
				configurations++
				if failure == "credentials" && configurations == 1 {
					return errors.New("hostd unavailable before runtime switch")
				}
				joining.cfg.DB = shared
				if failure == "catchup" {
					joining.cfg.DB = laggingLabDatabase{shared}
				}
				return nil
			}, Promote: func(context.Context, JoinBundle) error {
				promotions++
				if failure == "promotion" && promotions == 1 {
					return errors.New("promotion applied but response lost")
				}
				return nil
			}}
			cfg.ApplyTrust = func(ctx context.Context, members []Member) error {
				if configurations == 0 {
					if !joining.JoinPending() || len(members) != 2 {
						t.Fatal("missing pending enrollment or authenticated joining peers", members)
					}
					refreshed := make(chan error, 1)
					go func() { refreshed <- joining.RefreshTrust(ctx) }()
					if err := <-refreshed; err == nil {
						t.Fatal("external refresh replaced staged enrollment trust")
					}
					if _, err := joining.CreateInvitation(ctx); err == nil {
						t.Fatal("pending enrollment allowed a membership mutation")
					}
					if len(joining.Snapshot().Members) != 2 {
						t.Fatal("external refresh restored standalone membership")
					}
					stagedTrust = true
				}
				return nil
			}
			cfg.SyncConfig = func(context.Context) error {
				if !joining.JoinPending() {
					t.Fatal("authentication unfenced before joined configuration synchronized")
				}
				configSyncs++
				if failure == "config" && configSyncs == 1 {
					return errors.New("configuration cache unavailable")
				}
				return nil
			}
			joining, err = Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			req := JoinRequest{Invitation: inv, PeerURL: cfg.PeerURL, RaftAddress: cfg.RaftAddress, RQLiteAddress: cfg.RQLiteAddress}
			if _, err = joining.Join(ctx, req); err == nil {
				t.Fatal("expected interrupted enrollment")
			}
			if !joining.JoinPending() {
				t.Fatal("failed enrollment lost durable provisioning fence")
			}
			if _, err := joining.enroll(ctx, enrollmentRequest{}); err == nil || !strings.Contains(err.Error(), "pending") {
				t.Fatal("pending appliance accepted another membership enrollment", err)
			}
			if failure == "catchup" {
				row, err := shared.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id='joining'`})
				if err != nil || len(row.Values) != 1 || promotions != 0 || removals != 0 {
					t.Fatal("leader enrollment must not substitute for local catch-up", err, promotions, removals)
				}
			}
			if failure != "credentials" {
				cfg.DB = shared
			}
			if failure == "credentials" {
				if err = os.WriteFile(filepath.Join(cfg.DataDir, "node-key.pem"), []byte("interrupted write"), 0600); err != nil {
					t.Fatal(err)
				}
				if err = PreparePendingIdentity(cfg.DataDir); err != nil {
					t.Fatal(err)
				}
				if _, err = tls.LoadX509KeyPair(filepath.Join(cfg.DataDir, "node.pem"), filepath.Join(cfg.DataDir, "node-key.pem")); err != nil {
					t.Fatal("pending enrollment identity not repaired", err)
				}
			}
			joining, err = Open(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if !joining.JoinPending() {
				t.Fatal("restart lost durable enrollment fence")
			}
			if err = joining.ResumeJoin(ctx); err != nil {
				t.Fatal(err)
			}
			if joining.JoinPending() {
				t.Fatal("successful enrollment retained provisioning fence")
			}
			if removals != 1 {
				t.Fatalf("removed membership %d times; replay must not remove a promoted voter", removals)
			}
			if err = joining.ResumeJoin(ctx); err != nil {
				t.Fatal(err)
			}
			wantPromotions := 1
			if failure == "promotion" {
				wantPromotions = 2
			}
			if promotions != wantPromotions {
				t.Fatalf("completed join repeated promotion: %d", promotions)
			}
			wantSyncs := 1
			if failure == "config" {
				wantSyncs = 2
			}
			if configSyncs != wantSyncs {
				t.Fatalf("joined configuration synchronization count: got %d, want %d", configSyncs, wantSyncs)
			}
		})
	}
}

func TestVerifiedBackupRejectsSymlinksAndCorruption(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "db"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "backup")
	if err := BackupDirectory(source, dest); err != nil {
		t.Fatal(err)
	}
	if err := BackupDirectory(source, dest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "db"), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := BackupDirectory(source, dest); err == nil {
		t.Fatal("accepted damaged recovery backup")
	}
	if err := os.Symlink("db", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := BackupDirectory(source, filepath.Join(root, "backup2")); err == nil {
		t.Fatal("followed source symlink")
	}
}

func TestRecoveryRequiresAllExcludedAndRetainsTombstones(t *testing.T) {
	s, db := testLab(t)
	ctx := context.Background()
	state := s.Snapshot()
	state.Members = append(state.Members, Member{NodeID: "second", Status: "active", RaftAddress: "127.0.0.2:4002"})
	s.state = state
	if err := s.saveState(); err != nil {
		t.Fatal(err)
	}
	rqliteDir := filepath.Join(t.TempDir(), "rqlite")
	if err := os.MkdirAll(filepath.Join(rqliteDir, "raft"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rqliteDir, "raft", "raft.db"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	stops, starts := 0, 0
	cfg := RecoveryConfig{LabDataDir: s.cfg.DataDir, RQLiteDataDir: rqliteDir, JournalDir: filepath.Join(t.TempDir(), "journal"), DB: db, Stop: func(context.Context) error { stops++; return nil }, Start: func(context.Context) error { starts++; return nil }, Ready: func(context.Context) error { return nil }, ApplyTrust: func(context.Context, []Member) error { return nil }, FencePeers: func(context.Context, bool) error { return nil }}
	req := RecoveryRequest{OperationID: "recover-one", SurvivorID: "owner", ExcludedNodeIDs: []string{"second"}}
	if _, err := RecoverLab(ctx, cfg, req); err == nil {
		t.Fatal("recovery did not require fencing")
	}
	if stops != 0 {
		t.Fatal("stopped rqlite before confirmation")
	}
	req.ConfirmFenced = true
	status, err := RecoverLab(ctx, cfg, req)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "complete" || starts != 1 {
		t.Fatal(status, starts)
	}
	data, err := os.ReadFile(filepath.Join(rqliteDir, "raft", "peers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var peers []map[string]any
	if err = json.Unmarshal(data, &peers); err != nil || len(peers) != 1 || peers[0]["id"] != "owner" {
		t.Fatal("incorrect recovery peers", string(data), err)
	}
	if _, err = RecoverLab(ctx, cfg, req); err != nil || starts != 1 {
		t.Fatal("recovery retry restarted recovered database", err)
	}
	if err = s.RefreshTrust(ctx); err != nil {
		t.Fatal(err)
	}
	for _, m := range s.Snapshot().Members {
		if m.NodeID == "second" && m.Status != "removed" {
			t.Fatal("missing removed-node tombstone")
		}
	}
}

func TestSSHTrustIsRestrictedAndRemovalRevokes(t *testing.T) {
	dir := t.TempDir()
	_, pub, err := EnsureSSHIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	member := Member{NodeID: "peer", PeerURL: "https://127.0.0.2:9443", Status: "active", SSHHostKey: pub, SSHReplicationKey: pub}
	authorized := filepath.Join(dir, "authorized")
	known := filepath.Join(dir, "known")
	if err = ApplySSHTrust([]Member{member}, authorized, known, "/fixed/receiver"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(authorized)
	if !strings.Contains(string(data), `restrict,command="/fixed/receiver peer"`) {
		t.Fatal(string(data))
	}
	data, _ = os.ReadFile(known)
	for _, alias := range []string{"127.0.0.2", "peer"} {
		if !strings.Contains(string(data), alias+" "+pub) {
			t.Fatalf("missing pinned SSH alias %s: %s", alias, data)
		}
	}
	member.Status = "removed"
	if err = ApplySSHTrust([]Member{member}, authorized, known, "/fixed/receiver"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(authorized)
	if len(data) != 0 {
		t.Fatal("removed SSH key remained")
	}
	data, _ = os.ReadFile(known)
	if len(data) != 0 {
		t.Fatal("removed host key aliases remained")
	}
	member.Status = "active"
	member.SSHReplicationKey = "command=\"shell\" " + pub
	if err = ApplySSHTrust([]Member{member}, authorized, known, "/fixed/receiver"); err == nil {
		t.Fatal("accepted supplied authorized key options")
	}
}

func TestHTTPRejectsUnknownJSONFields(t *testing.T) {
	s, _ := testLab(t)
	r := httptest.NewRequest(http.MethodPost, "/join", strings.NewReader(`{"unexpected":true}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestRotationWaitsForPeerTrustBeforeRevokingOldIdentity(t *testing.T) {
	s, db := testLab(t)
	ctx := context.Background()
	initial, err := s.TLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	oldCert, err := x509.ParseCertificate(initial.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	peer := Member{NodeID: "second", PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001", Status: "active"}
	if err = s.putMember(ctx, peer); err != nil {
		t.Fatal(err)
	}
	rotations := 0
	s.cfg.Rotate = func(context.Context, JoinBundle) error { rotations++; return nil }
	req := RotationRequest{OperationID: "rotate-one"}
	status, err := s.RotateIdentity(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "awaiting-trust" || len(status.PendingMembers) != 1 || status.PendingMembers[0] != "second" {
		t.Fatal(status)
	}
	if err = os.WriteFile(filepath.Join(s.cfg.DataDir, "node-key.pem"), []byte("interrupted rotation write"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = PreparePendingIdentity(s.cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	if _, err = tls.LoadX509KeyPair(filepath.Join(s.cfg.DataDir, "node.pem"), filepath.Join(s.cfg.DataDir, "node-key.pem")); err != nil {
		t.Fatal("pending rotation identity not repaired", err)
	}
	if err = s.CheckPeer(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{oldCert}}); err != nil {
		t.Fatal("old cert revoked before peer catch-up", err)
	}
	if err = db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO lab_trust_applied(node_id,revision) VALUES(?,?)`, Params: []interface{}{"second", status.TrustRevision}}}); err != nil {
		t.Fatal(err)
	}
	status, err = s.RotateIdentity(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "complete" || rotations != 1 {
		t.Fatal(status, rotations)
	}
	if err = s.CheckPeer(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{oldCert}}); err == nil {
		t.Fatal("old identity survived completed rotation")
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "rotation-rotate-one.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("completed rotation retained transient secrets")
	}
}

func TestTombstoneAddressReleaseRequiresFencingAndReset(t *testing.T) {
	s, _ := testLab(t)
	ctx := context.Background()
	m := Member{NodeID: "removed", PeerURL: "https://127.0.0.2:9443", RaftAddress: "127.0.0.2:4002", RQLiteAddress: "127.0.0.2:4001", Status: "removed"}
	if err := s.putMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	req := ReleasePeerAddressRequest{OperationID: "release-one", ConfirmNodeID: m.NodeID, ConfirmFenced: true}
	if _, err := s.ReleasePeerAddress(ctx, m.NodeID, req); err == nil {
		t.Fatal("released peer address without reset confirmation")
	}
	req.ConfirmReset = true
	released, err := s.ReleasePeerAddress(ctx, m.NodeID, req)
	if err != nil {
		t.Fatal(err)
	}
	if !released.PeerAddressReleased || released.Status != "removed" {
		t.Fatal("release must retain tombstone", released)
	}
	preview, err := s.RemovalPreview(ctx, m.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Stale || len(preview.Warnings) == 0 || preview.Member.NodeID != m.NodeID {
		t.Fatal("missing inventory must not appear authoritative", preview)
	}
}
