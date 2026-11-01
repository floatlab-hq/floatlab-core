package lab

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
)

type Database interface {
	Execute(context.Context, []rqlite.Statement) error
	Query(context.Context, rqlite.Statement) (*rqlite.QueryResult, error)
	QueryStrong(context.Context, rqlite.Statement) (*rqlite.QueryResult, error)
	QueryLocal(context.Context, rqlite.Statement) (*rqlite.QueryResult, error)
	Request(context.Context, rqlite.Statement) (*rqlite.QueryResult, error)
}

var ErrUnavailable = errors.New("lab quorum unavailable")

type Member struct {
	NodeID                         string `json:"node_id"`
	PeerURL                        string `json:"peer_url"`
	RaftAddress                    string `json:"raft_address"`
	RQLiteAddress                  string `json:"rqlite_address"`
	CertificateFingerprint         string `json:"certificate_fingerprint"`
	PreviousCertificateFingerprint string `json:"previous_certificate_fingerprint,omitempty"`
	Status                         string `json:"status"`
	OperationID                    string `json:"operation_id,omitempty"`
	RemovedAt                      string `json:"removed_at,omitempty"`
	SSHHostKey                     string `json:"ssh_host_key,omitempty"`
	SSHReplicationKey              string `json:"ssh_replication_key,omitempty"`
	PreviousSSHReplicationKey      string `json:"previous_ssh_replication_key,omitempty"`
	PeerAddressReleased            bool   `json:"peer_address_released,omitempty"`
	AppliedTrustRevision           int64  `json:"applied_trust_revision"`
}

type Invitation struct {
	LabID        string    `json:"lab_id"`
	BootstrapURL string    `json:"bootstrap_url"`
	Token        string    `json:"token"`
	Fingerprint  string    `json:"fingerprint"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type JoinRequest struct {
	Invitation    Invitation `json:"invitation"`
	PeerURL       string     `json:"peer_url"`
	RaftAddress   string     `json:"raft_address"`
	RQLiteAddress string     `json:"rqlite_address"`
}

// JoinBundle contains only the joining node's identity; it never contains another
// node's private key. Paths are appliance-local, not supplied by the caller.
type JoinBundle struct {
	LabID                string            `json:"lab_id"`
	NodeID               string            `json:"node_id"`
	BootstrapRaftAddress string            `json:"bootstrap_raft_address"`
	PeerURL              string            `json:"peer_url"`
	RaftAddress          string            `json:"raft_address"`
	RQLiteAddress        string            `json:"rqlite_address"`
	CAPath               string            `json:"ca_path"`
	CertificatePath      string            `json:"certificate_path"`
	PrivateKeyPath       string            `json:"private_key_path"`
	CAPEM                string            `json:"ca_pem"`
	CertificatePEM       string            `json:"certificate_pem"`
	PrivateKeyPEM        string            `json:"private_key_pem"`
	Secrets              map[string]string `json:"secrets,omitempty"`
}

type Config struct {
	NodeID, PeerURL, RaftAddress, RQLiteAddress, DataDir, RQLiteURL string
	DB                                                              Database
	RequireEmpty                                                    func(context.Context) error
	ApplyJoin                                                       func(context.Context, JoinBundle) error
	Promote                                                         func(context.Context, JoinBundle) error
	ExportSecrets                                                   func(context.Context) (map[string]string, error)
	ImportSecrets                                                   func(context.Context, map[string]string) error
	SyncConfig                                                      func(context.Context) error
	ApplyTrust                                                      func(context.Context, []Member) error
	SSHHostKey                                                      string
	SSHReplicationKey                                               string
	HTTPClient                                                      *http.Client
	LocalDB                                                         *rqlite.Client
	ReplicaDir                                                      string
	Rotate                                                          func(context.Context, JoinBundle) error
}

type State struct {
	LabID                     string   `json:"lab_id"`
	NodeID                    string   `json:"node_id"`
	Members                   []Member `json:"members"`
	TrustRevision             int64    `json:"trust_revision"`
	AppliedTrustRevision      int64    `json:"applied_trust_revision"`
	AcknowledgedTrustRevision int64    `json:"acknowledged_trust_revision"`
}

type Service struct {
	cfg        Config
	mu         sync.RWMutex
	state      State
	operations sync.Mutex
	trustMu    sync.Mutex
	joining    atomic.Bool
}

func Open(ctx context.Context, cfg Config) (*Service, error) {
	if !validID.MatchString(cfg.NodeID) || cfg.DB == nil || cfg.DataDir == "" {
		return nil, errors.New("node ID, lab database and private data directory required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, err
	}
	s := &Service{cfg: cfg}
	data, err := os.ReadFile(filepath.Join(cfg.DataDir, "membership.json"))
	if err == nil {
		if err = json.Unmarshal(data, &s.state); err != nil {
			return nil, err
		}
		if s.state.NodeID != cfg.NodeID {
			return nil, errors.New("persisted node identity mismatch")
		}
		// Local cached membership keeps workload authentication independent of quorum.
		if err = s.RefreshTrust(ctx); err != nil {
			return s, nil
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err = s.migrate(ctx); err != nil {
		return nil, err
	}
	s.state = State{LabID: uuid.NewString(), NodeID: cfg.NodeID}
	ca, key, err := newCA(s.state.LabID)
	if err != nil {
		return nil, err
	}
	nodeKey, csr, err := keyAndCSR(cfg.NodeID)
	if err != nil {
		return nil, err
	}
	cert, err := signCSR(ca, key, csr, s.state.LabID, cfg.NodeID, cfg.PeerURL, cfg.RaftAddress, cfg.RQLiteAddress)
	if err != nil {
		return nil, err
	}
	if err = s.installIdentity(ca, cert, nodeKey); err != nil {
		return nil, err
	}
	if err = update.WriteFile(filepath.Join(cfg.DataDir, "ca-key.pem"), key, 0600); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(cert)
	m := Member{NodeID: cfg.NodeID, PeerURL: cfg.PeerURL, RaftAddress: cfg.RaftAddress, RQLiteAddress: cfg.RQLiteAddress, CertificateFingerprint: fingerprint(block.Bytes), Status: "active", SSHHostKey: cfg.SSHHostKey, SSHReplicationKey: cfg.SSHReplicationKey}
	if err = s.putMember(ctx, m); err != nil {
		return nil, err
	}
	s.state.Members = []Member{m}
	if err = s.saveState(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) migrate(ctx context.Context) error {
	return s.cfg.DB.Execute(ctx, []rqlite.Statement{
		{SQL: `CREATE TABLE IF NOT EXISTS nodes (id TEXT PRIMARY KEY,cluster_uuid TEXT NOT NULL,name TEXT NOT NULL UNIQUE,addresses TEXT NOT NULL DEFAULT '[]',created_at DATETIME NOT NULL,updated_at DATETIME NOT NULL,hostname TEXT NOT NULL DEFAULT '',role TEXT NOT NULL DEFAULT 'secondary',zfs_pool TEXT NOT NULL DEFAULT 'floatlab')`},
		{SQL: `CREATE TABLE IF NOT EXISTS lab_members (node_id TEXT PRIMARY KEY, body TEXT NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS lab_trust_meta (id INTEGER PRIMARY KEY CHECK(id=1),revision INTEGER NOT NULL)`},
		{SQL: `INSERT OR IGNORE INTO lab_trust_meta(id,revision) VALUES(1,0)`},
		{SQL: `CREATE TABLE IF NOT EXISTS lab_trust_applied(node_id TEXT PRIMARY KEY,revision INTEGER NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS lab_invitations (token_hash TEXT PRIMARY KEY, lab_id TEXT NOT NULL, expires_at TEXT NOT NULL, claimed_by TEXT, csr_hash TEXT, response TEXT)`},
	})
}

func (s *Service) saveState() error {
	return update.WriteJSON(filepath.Join(s.cfg.DataDir, "membership.json"), s.state)
}
func (s *Service) putMember(ctx context.Context, m Member) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	u, err := url.Parse(m.PeerURL)
	if err != nil {
		return err
	}
	addresses, _ := json.Marshal([]map[string]string{{"interface": "peer", "type": "LAN-6", "address": u.Hostname(), "netmask": ""}})
	return s.cfg.DB.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO lab_members(node_id,body) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET body=excluded.body`, Params: []interface{}{m.NodeID, string(b)}},
		{SQL: `UPDATE lab_trust_meta SET revision=revision+1 WHERE id=1`},
		{SQL: `INSERT INTO nodes(id,cluster_uuid,name,addresses,hostname,role,zfs_pool,created_at,updated_at) VALUES(?,?,?,?,?,'secondary','floatlab',?,?) ON CONFLICT(id) DO UPDATE SET cluster_uuid=excluded.cluster_uuid,addresses=excluded.addresses,updated_at=excluded.updated_at`, Params: []interface{}{m.NodeID, s.Snapshot().LabID, m.NodeID, string(addresses), m.NodeID, time.Now().UTC().Format(time.RFC3339Nano), time.Now().UTC().Format(time.RFC3339Nano)}},
	})
}
func (s *Service) Snapshot() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copy := s.state
	copy.Members = append([]Member(nil), s.state.Members...)
	return copy
}

// InitialBundle supplies hostd with the local standalone identity when peer
// networking is first enabled. It does not rotate or copy a peer private key.
func (s *Service) InitialBundle() (JoinBundle, error) {
	state := s.Snapshot()
	bundle := JoinBundle{LabID: state.LabID, NodeID: state.NodeID, PeerURL: s.cfg.PeerURL, RaftAddress: s.cfg.RaftAddress, RQLiteAddress: s.cfg.RQLiteAddress, CAPath: filepath.Join(s.cfg.DataDir, "ca.pem"), CertificatePath: filepath.Join(s.cfg.DataDir, "node.pem"), PrivateKeyPath: filepath.Join(s.cfg.DataDir, "node-key.pem")}
	for path, target := range map[string]*string{bundle.CAPath: &bundle.CAPEM, bundle.CertificatePath: &bundle.CertificatePEM, bundle.PrivateKeyPath: &bundle.PrivateKeyPEM} {
		data, err := os.ReadFile(path)
		if err != nil {
			return bundle, err
		}
		*target = string(data)
	}
	return bundle, nil
}

// JoinPending also covers a failed or interrupted enrollment after restart.
func (s *Service) JoinPending() bool {
	if s.joining.Load() {
		return true
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "join.json"))
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	if err != nil {
		return true
	}
	var journal joinJournal
	return json.Unmarshal(data, &journal) != nil || journal.Phase != "complete"
}

func (s *Service) RefreshTrust(ctx context.Context) error {
	return s.refreshTrust(ctx, false)
}

func (s *Service) refreshTrust(ctx context.Context, duringJoin bool) error {
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if !duringJoin && s.JoinPending() {
		return errors.New("lab enrollment is pending; committed trust refresh deferred")
	}
	r, err := s.cfg.DB.Query(ctx, rqlite.Statement{SQL: `SELECT body,(SELECT revision FROM lab_trust_meta WHERE id=1),coalesce((SELECT revision FROM lab_trust_applied WHERE node_id=lab_members.node_id),0) FROM lab_members ORDER BY node_id`})
	if err != nil {
		return err
	}
	members := make([]Member, 0, len(r.Values))
	var revision int64
	for _, row := range r.Values {
		if len(row) != 3 {
			return errors.New("invalid member record")
		}
		var m Member
		if err = json.Unmarshal([]byte(fmt.Sprint(row[0])), &m); err != nil {
			return err
		}
		if revision, err = strconv.ParseInt(fmt.Sprint(row[1]), 10, 64); err != nil {
			return err
		}
		if m.AppliedTrustRevision, err = strconv.ParseInt(fmt.Sprint(row[2]), 10, 64); err != nil {
			return err
		}
		if (m.Status == "removing" || m.Status == "removed") && s.cfg.LocalDB != nil {
			if err := config.NewStore(s.cfg.LocalDB).DisableBackupNode(ctx, m.NodeID); err != nil {
				return fmt.Errorf("persist backup cleanup: %w", err)
			}
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		return errors.New("lab membership is empty")
	}
	previous := s.Snapshot()
	if revision < previous.AppliedTrustRevision {
		return errors.New("refusing older cached trust revision")
	}
	if s.cfg.ApplyTrust != nil {
		if err = s.cfg.ApplyTrust(ctx, members); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.state.Members = members
	s.state.TrustRevision = revision
	s.state.AppliedTrustRevision = revision
	err = s.saveState()
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if previous.AcknowledgedTrustRevision < revision {
		if err = s.cfg.DB.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO lab_trust_applied(node_id,revision) VALUES(?,?) ON CONFLICT(node_id) DO UPDATE SET revision=excluded.revision WHERE revision<excluded.revision`, Params: []interface{}{s.cfg.NodeID, revision}}}); err != nil {
			return nil
		}
		s.mu.Lock()
		s.state.AcknowledgedTrustRevision = revision
		for i := range s.state.Members {
			if s.state.Members[i].NodeID == s.cfg.NodeID {
				s.state.Members[i].AppliedTrustRevision = revision
			}
		}
		err = s.saveState()
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Service) CreateInvitation(ctx context.Context) (Invitation, error) {
	if err := s.RefreshTrust(ctx); err != nil {
		return Invitation{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	s.mu.RLock()
	labID := s.state.LabID
	s.mu.RUnlock()
	ca, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca.pem"))
	if err != nil {
		return Invitation{}, err
	}
	if _, err = os.Stat(filepath.Join(s.cfg.DataDir, "ca-key.pem")); err != nil {
		return Invitation{}, errors.New("invitations must be issued by the lab enrollment authority")
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return Invitation{}, err
	}
	i := Invitation{LabID: labID, BootstrapURL: s.cfg.PeerURL, Token: hex.EncodeToString(b), Fingerprint: fingerprintPEM(ca), ExpiresAt: time.Now().UTC().Add(15 * time.Minute)}
	sum := sha256.Sum256([]byte(i.Token))
	err = s.cfg.DB.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO lab_invitations(token_hash,lab_id,expires_at) VALUES(?,?,?)`, Params: []interface{}{hex.EncodeToString(sum[:]), labID, i.ExpiresAt.Format(time.RFC3339Nano)}}})
	if err != nil {
		return Invitation{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return i, err
}

func fingerprintPEM(b []byte) string {
	block, _ := pem.Decode(b)
	if block == nil {
		return ""
	}
	return fingerprint(block.Bytes)
}

func (s *Service) Remove(ctx context.Context, nodeID, confirm, operationID string) (Member, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if nodeID != confirm || !validID.MatchString(nodeID) || operationID == "" {
		return Member{}, errors.New("explicit matching node confirmation and operation ID required")
	}
	if nodeID == s.cfg.NodeID {
		return Member{}, errors.New("cannot remove the local coordinator; use another healthy member")
	}
	// A strong read verifies quorum before the first membership mutation.
	r, err := s.cfg.DB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id=?`, Params: []interface{}{nodeID}})
	if err != nil {
		return Member{}, fmt.Errorf("membership removal requires quorum; use explicit root lab recovery after fencing: %w", err)
	}
	if len(r.Values) != 1 {
		return Member{}, errors.New("unknown lab member")
	}
	var m Member
	if err = json.Unmarshal([]byte(fmt.Sprint(r.Values[0][0])), &m); err != nil {
		return m, err
	}
	if m.Status == "removed" {
		return m, nil
	}
	if m.OperationID != "" && m.OperationID != operationID {
		return m, errors.New("resume removal using its original operation ID")
	}
	m.Status = "removing"
	m.OperationID = operationID
	if err = s.putMember(ctx, m); err != nil {
		return m, err
	}
	if err = update.WriteJSON(filepath.Join(s.cfg.DataDir, "removal-"+nodeID+".json"), m); err != nil {
		return m, err
	}
	if err = s.RefreshTrust(ctx); err != nil {
		return m, err
	}
	if err = s.removeRQLite(ctx, nodeID); err != nil {
		return m, err
	}
	m.Status = "removed"
	m.RemovedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = s.putMember(ctx, m); err != nil {
		return m, err
	}
	if err = s.RefreshTrust(ctx); err != nil {
		return m, err
	}
	err = update.WriteJSON(filepath.Join(s.cfg.DataDir, "removal-"+nodeID+".json"), m)
	return m, err
}

func (s *Service) removeRQLite(ctx context.Context, nodeID string) error {
	payload, _ := json.Marshal(map[string]string{"id": nodeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(s.cfg.RQLiteURL, "/")+"/remove", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rqlite membership removal failed: HTTP %d", resp.StatusCode)
	}
	return nil
}
