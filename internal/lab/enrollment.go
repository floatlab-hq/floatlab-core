package lab

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type enrollmentRequest struct {
	Token             string `json:"token"`
	NodeID            string `json:"node_id"`
	PeerURL           string `json:"peer_url"`
	RaftAddress       string `json:"raft_address"`
	RQLiteAddress     string `json:"rqlite_address"`
	CSR               string `json:"csr"`
	SSHHostKey        string `json:"ssh_host_key"`
	SSHReplicationKey string `json:"ssh_replication_key"`
	RetryTimestamp    int64  `json:"retry_timestamp,omitempty"`
	RetrySignature    string `json:"retry_signature,omitempty"`
}
type enrollmentResponse struct {
	LabID                string            `json:"lab_id"`
	CAPEM                string            `json:"ca_pem"`
	CertificatePEM       string            `json:"certificate_pem"`
	BootstrapRaftAddress string            `json:"bootstrap_raft_address"`
	Members              []Member          `json:"members"`
	Secrets              map[string]string `json:"secrets,omitempty"`
	AuthorityKeyPEM      string            `json:"authority_key_pem,omitempty"`
}
type joinJournal struct {
	Request         JoinRequest `json:"request"`
	Phase           string      `json:"phase"`
	Bundle          JoinBundle  `json:"bundle"`
	Members         []Member    `json:"members"`
	AuthorityKeyPEM string      `json:"authority_key_pem,omitempty"`
}

func (s *Service) enroll(ctx context.Context, req enrollmentRequest) (enrollmentResponse, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if s.JoinPending() {
		return enrollmentResponse{}, errors.New("lab enrollment is pending; cannot enroll another member")
	}
	if !validID.MatchString(req.NodeID) || len(req.Token) != 64 {
		return enrollmentResponse{}, errors.New("invalid enrollment identity or invitation")
	}
	sum := sha256.Sum256([]byte(req.Token))
	tokenHash := hex.EncodeToString(sum[:])
	csrSum := sha256.Sum256([]byte(req.CSR))
	csrHash := hex.EncodeToString(csrSum[:])
	r, err := s.cfg.DB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT lab_id,expires_at,claimed_by,csr_hash,response FROM lab_invitations WHERE token_hash=?`, Params: []interface{}{tokenHash}})
	if err != nil {
		return enrollmentResponse{}, err
	}
	if len(r.Values) != 1 || len(r.Values[0]) != 5 {
		return enrollmentResponse{}, errors.New("invalid invitation")
	}
	row := r.Values[0]
	expires, err := time.Parse(time.RFC3339Nano, fmt.Sprint(row[1]))
	replaying := fmt.Sprint(row[2]) == req.NodeID && fmt.Sprint(row[3]) == csrHash && row[4] != nil && fmt.Sprint(row[4]) != ""
	if err != nil || (time.Now().After(expires) && (!replaying || !verifyEnrollmentRetry(req))) {
		return enrollmentResponse{}, errors.New("invitation expired")
	}
	if fmt.Sprint(row[0]) != s.Snapshot().LabID {
		return enrollmentResponse{}, errors.New("invitation lab identity mismatch")
	}
	if row[2] != nil && fmt.Sprint(row[2]) != "" && fmt.Sprint(row[2]) != req.NodeID {
		return enrollmentResponse{}, errors.New("invitation already used")
	}
	if row[3] != nil && fmt.Sprint(row[3]) != "" && fmt.Sprint(row[3]) != csrHash {
		return enrollmentResponse{}, errors.New("invitation already bound to another key")
	}
	var response enrollmentResponse
	if row[4] != nil && fmt.Sprint(row[4]) != "" {
		if err = json.Unmarshal([]byte(fmt.Sprint(row[4])), &response); err != nil {
			return response, err
		}
	} else {
		for _, m := range s.Snapshot().Members {
			if m.NodeID == req.NodeID {
				return response, errors.New("node ID already used; removed IDs cannot re-enroll")
			}
			oldURL, _ := url.Parse(m.PeerURL)
			newURL, _ := url.Parse(req.PeerURL)
			if oldURL != nil && newURL != nil && oldURL.Hostname() == newURL.Hostname() && (m.Status != "removed" || !m.PeerAddressReleased) {
				return response, errors.New("peer address is already assigned or quarantined; explicitly release the fenced and reset tombstone first")
			}
		}
		ca, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca.pem"))
		if err != nil {
			return response, err
		}
		key, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca-key.pem"))
		if err != nil {
			return response, errors.New("this node is not the enrollment authority")
		}
		cert, err := signCSR(ca, key, []byte(req.CSR), s.Snapshot().LabID, req.NodeID, req.PeerURL, req.RaftAddress, req.RQLiteAddress)
		if err != nil {
			return response, err
		}
		if req.SSHHostKey != "" {
			if err = validateSSHKey(req.SSHHostKey); err != nil {
				return response, err
			}
		}
		if req.SSHReplicationKey != "" {
			if err = validateSSHKey(req.SSHReplicationKey); err != nil {
				return response, err
			}
		}
		m := Member{NodeID: req.NodeID, PeerURL: req.PeerURL, RaftAddress: req.RaftAddress, RQLiteAddress: req.RQLiteAddress, CertificateFingerprint: fingerprintPEM(cert), Status: "enrolling", SSHHostKey: req.SSHHostKey, SSHReplicationKey: req.SSHReplicationKey}
		response = enrollmentResponse{LabID: s.Snapshot().LabID, CAPEM: string(ca), CertificatePEM: string(cert), BootstrapRaftAddress: s.cfg.RaftAddress, Members: append(s.Snapshot().Members, m)}
		// Never persist exported JWT or encryption keys in the invitation response.
		body, _ := json.Marshal(response)
		claim, err := s.cfg.DB.Request(ctx, rqlite.Statement{SQL: `UPDATE lab_invitations SET claimed_by=?,csr_hash=?,response=? WHERE token_hash=? AND claimed_by IS NULL RETURNING token_hash`, Params: []interface{}{req.NodeID, csrHash, string(body), tokenHash}})
		if err != nil {
			return response, err
		}
		if len(claim.Values) != 1 {
			return response, errors.New("invitation claimed concurrently; retry")
		}
		if err = s.putMember(ctx, m); err != nil {
			return response, err
		}
	}
	// An interrupted putMember is retried from the durable invitation claim.
	for _, m := range response.Members {
		if m.NodeID == req.NodeID {
			current, e := s.cfg.DB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id=?`, Params: []interface{}{m.NodeID}})
			if e != nil {
				return response, e
			}
			if len(current.Values) > 0 {
				var existing Member
				if e = json.Unmarshal([]byte(fmt.Sprint(current.Values[0][0])), &existing); e != nil {
					return response, e
				}
				if existing.Status == "removed" || existing.Status == "removing" {
					return response, errors.New("removed enrollment identity")
				}
				if existing.Status == "active" {
					continue
				}
			}
			if err = s.putMember(ctx, m); err != nil {
				return response, err
			}
		}
	}
	if err = s.RefreshTrust(ctx); err != nil {
		return response, err
	}
	if s.cfg.ExportSecrets != nil {
		response.Secrets, err = s.cfg.ExportSecrets(ctx)
	}
	if err != nil {
		return response, err
	}
	// Every enrolled appliance is a trusted lab administrator. Replicate the
	// signing authority only over the pinned TLS enrollment response; never put
	// it in the invitation table or copy another node's leaf private key.
	caKey, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca-key.pem"))
	if err != nil {
		return response, err
	}
	response.AuthorityKeyPEM = string(caKey)
	return response, err
}

// PreparePendingIdentity repairs an interrupted identity write before any
// startup TLS client attempts to load the certificate/key pair.
func PreparePendingIdentity(dir string) error {
	if err := preparePendingJoinIdentity(dir); err != nil {
		return err
	}
	paths, err := filepath.Glob(filepath.Join(dir, "rotation-*.json"))
	if err != nil {
		return err
	}
	var pending *rotationJournal
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var journal rotationJournal
		if err = json.Unmarshal(data, &journal); err != nil {
			return err
		}
		if journal.Status.Phase != "authorized" && journal.Status.Phase != "awaiting-trust" {
			continue
		}
		if pending != nil {
			return errors.New("multiple pending credential rotations")
		}
		pending = &journal
	}
	if pending == nil {
		return nil
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return err
	}
	s := &Service{cfg: Config{DataDir: dir}}
	if err = s.installIdentity(ca, []byte(pending.CertificatePEM), []byte(pending.PrivateKeyPEM)); err != nil {
		return err
	}
	return update.WriteFile(filepath.Join(dir, "replication-key"), []byte(pending.SSHPrivateKeyPEM), 0600)
}

func preparePendingJoinIdentity(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "join.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal joinJournal
	if err = json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if journal.Phase != "credentials" {
		return nil
	}
	s := &Service{cfg: Config{DataDir: dir}}
	return s.installIdentity([]byte(journal.Bundle.CAPEM), []byte(journal.Bundle.CertificatePEM), []byte(journal.Bundle.PrivateKeyPEM))
}

// ResumeJoin reconciles a pending identity/runtime switch before normal startup.
func (s *Service) ResumeJoin(ctx context.Context) error {
	data, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "join.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal joinJournal
	if err = json.Unmarshal(data, &journal); err != nil {
		return err
	}
	if journal.Phase == "complete" {
		return nil
	}
	_, err = s.Join(ctx, journal.Request)
	return err
}

// Join runs on the empty joining appliance. ApplyJoin must preserve the original
// rqlite directory and start the pinned binary as a non-voter; Promote restarts
// it with the same join address as a voter after catch-up has been verified.
func (s *Service) Join(ctx context.Context, req JoinRequest) (Member, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	// Drain any refresh that started against standalone configuration before
	// publishing the enrollment fence used by API and background workers.
	s.trustMu.Lock()
	s.joining.Store(true)
	s.trustMu.Unlock()
	defer s.joining.Store(false)
	if s.cfg.RequireEmpty == nil || s.cfg.ApplyJoin == nil || s.cfg.Promote == nil {
		return Member{}, errors.New("enrollment runtime is not configured")
	}
	journalPath := filepath.Join(s.cfg.DataDir, "join.json")
	var journal joinJournal
	if data, err := os.ReadFile(journalPath); err == nil {
		if err = json.Unmarshal(data, &journal); err != nil {
			return Member{}, err
		}
		before, _ := json.Marshal(journal.Request)
		after, _ := json.Marshal(req)
		if string(before) != string(after) {
			return Member{}, errors.New("resume or explicitly reset the pending enrollment")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Member{}, err
	} else {
		if err = s.cfg.RequireEmpty(ctx); err != nil {
			return Member{}, fmt.Errorf("joining appliance must be empty: %w", err)
		}
		if len(s.Snapshot().Members) != 1 {
			return Member{}, errors.New("cannot merge existing labs")
		}
		journal = joinJournal{Request: req, Phase: "validated"}
		if err = BackupDirectory(s.cfg.DataDir, filepath.Join(filepath.Dir(s.cfg.DataDir), "lab-before-join-"+s.Snapshot().LabID)); err != nil {
			return Member{}, err
		}
		if err = update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if journal.Phase == "validated" {
		keyPath := filepath.Join(s.cfg.DataDir, "join-key.pem")
		csrPath := filepath.Join(s.cfg.DataDir, "join-csr.pem")
		key, err := os.ReadFile(keyPath)
		var csr []byte
		if errors.Is(err, os.ErrNotExist) {
			key, csr, err = keyAndCSR(s.cfg.NodeID)
			if err != nil {
				return Member{}, err
			}
			if err = update.WriteFile(csrPath, csr, 0600); err != nil {
				return Member{}, err
			}
			if err = update.WriteFile(keyPath, key, 0600); err != nil {
				return Member{}, err
			}
		} else if err == nil {
			csr, err = os.ReadFile(csrPath)
		}
		if err != nil {
			return Member{}, err
		}
		enroll := enrollmentRequest{Token: req.Invitation.Token, NodeID: s.cfg.NodeID, PeerURL: req.PeerURL, RaftAddress: req.RaftAddress, RQLiteAddress: req.RQLiteAddress, CSR: string(csr), SSHHostKey: s.cfg.SSHHostKey, SSHReplicationKey: s.cfg.SSHReplicationKey}
		if err = signEnrollmentRetry(&enroll, key, time.Now().Unix()); err != nil {
			return Member{}, err
		}
		response, err := requestEnrollment(ctx, req.Invitation, enroll)
		if err != nil {
			return Member{}, err
		}
		if response.LabID != req.Invitation.LabID {
			return Member{}, errors.New("enrollment lab identity mismatch")
		}
		journal.Bundle = JoinBundle{LabID: response.LabID, NodeID: s.cfg.NodeID, BootstrapRaftAddress: response.BootstrapRaftAddress, PeerURL: req.PeerURL, RaftAddress: req.RaftAddress, RQLiteAddress: req.RQLiteAddress, CAPath: filepath.Join(s.cfg.DataDir, "ca.pem"), CertificatePath: filepath.Join(s.cfg.DataDir, "node.pem"), PrivateKeyPath: filepath.Join(s.cfg.DataDir, "node-key.pem"), CAPEM: response.CAPEM, CertificatePEM: response.CertificatePEM, PrivateKeyPEM: string(key), Secrets: response.Secrets}
		journal.Phase = "credentials"
		journal.Members = response.Members
		journal.AuthorityKeyPEM = response.AuthorityKeyPEM
		if err = update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if journal.Phase == "credentials" {
		if err := s.installIdentity([]byte(journal.Bundle.CAPEM), []byte(journal.Bundle.CertificatePEM), []byte(journal.Bundle.PrivateKeyPEM)); err != nil {
			return Member{}, err
		}
		s.mu.Lock()
		s.state = State{LabID: journal.Bundle.LabID, NodeID: s.cfg.NodeID, Members: journal.Members}
		saveErr := s.saveState()
		s.mu.Unlock()
		if saveErr != nil {
			return Member{}, saveErr
		}
		if _, err := tls.X509KeyPair([]byte(journal.Bundle.CAPEM), []byte(journal.AuthorityKeyPEM)); err != nil {
			return Member{}, fmt.Errorf("invalid enrollment signing authority: %w", err)
		}
		if err := update.WriteFile(filepath.Join(s.cfg.DataDir, "ca-key.pem"), []byte(journal.AuthorityKeyPEM), 0600); err != nil {
			return Member{}, err
		}
		if s.cfg.ImportSecrets != nil {
			if err := s.cfg.ImportSecrets(ctx, journal.Bundle.Secrets); err != nil {
				return Member{}, err
			}
		}
		if s.cfg.ApplyTrust != nil {
			if err := s.cfg.ApplyTrust(ctx, journal.Members); err != nil {
				return Member{}, err
			}
		}
		if err := s.cfg.ApplyJoin(ctx, journal.Bundle); err != nil {
			return Member{}, err
		}
		journal.Phase = "nonvoter"
		if err := update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if journal.Phase == "nonvoter" {
		// Read without forwarding so the local non-voter must have applied its
		// enrollment record before promotion raises quorum size.
		r, err := s.cfg.DB.QueryLocal(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id=?`, Params: []interface{}{s.cfg.NodeID}})
		if err != nil {
			return Member{}, fmt.Errorf("waiting for non-voter catch-up: %w", err)
		}
		if len(r.Values) != 1 {
			return Member{}, errors.New("waiting for non-voter to apply enrollment locally")
		}
		journal.Phase = "promotion-ready"
		if err = update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if journal.Phase == "promotion-ready" {
		// Pinned rqlite 8.26 ignores a join for an existing ID/address even if
		// voter=true. Remove the caught-up non-voter before rejoining as voter.
		// This does not reduce the original voting quorum.
		if err := s.removeRQLite(ctx, s.cfg.NodeID); err != nil {
			return Member{}, err
		}
		journal.Phase = "nonvoter-removed"
		if err := update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if journal.Phase == "nonvoter-removed" {
		if err := s.cfg.Promote(ctx, journal.Bundle); err != nil {
			return Member{}, err
		}
		journal.Phase = "promoted"
		if err := update.WriteJSON(journalPath, journal); err != nil {
			return Member{}, err
		}
	}
	if err := s.refreshTrust(ctx, true); err != nil {
		return Member{}, err
	}
	var member Member
	for _, m := range s.Snapshot().Members {
		if m.NodeID == s.cfg.NodeID {
			member = m
		}
	}
	if member.NodeID == "" {
		return Member{}, errors.New("local node missing from joined membership")
	}
	member.Status = "active"
	if err := s.putMember(ctx, member); err != nil {
		return member, err
	}
	if err := s.refreshTrust(ctx, true); err != nil {
		return member, err
	}
	if s.cfg.SyncConfig != nil {
		if err := s.cfg.SyncConfig(ctx); err != nil {
			return member, err
		}
	}
	journal.Phase = "complete"
	journal.Bundle.Secrets = nil
	journal.Bundle.PrivateKeyPEM = ""
	journal.AuthorityKeyPEM = ""
	if err := update.WriteJSON(journalPath, journal); err != nil {
		return member, err
	}
	return member, nil
}

func enrollmentRetryDigest(req enrollmentRequest) [sha256.Size]byte {
	tokenHash := sha256.Sum256([]byte(req.Token))
	csrHash := sha256.Sum256([]byte(req.CSR))
	return sha256.Sum256([]byte(fmt.Sprintf("FloatLab enrollment retry\n%x\n%s\n%x\n%d", tokenHash, req.NodeID, csrHash, req.RetryTimestamp)))
}

func signEnrollmentRetry(req *enrollmentRequest, keyPEM []byte, timestamp int64) error {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return errors.New("invalid enrollment retry private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return errors.New("enrollment retry requires an ECDSA P-256 key")
	}
	req.RetryTimestamp = timestamp
	digest := enrollmentRetryDigest(*req)
	signature, err := ecdsa.SignASN1(rand.Reader, key, digest[:])
	if err != nil {
		return err
	}
	req.RetrySignature = hex.EncodeToString(signature)
	return nil
}

func verifyEnrollmentRetry(req enrollmentRequest) bool {
	now := time.Now().Unix()
	if req.RetryTimestamp < now-300 || req.RetryTimestamp > now+300 {
		return false
	}
	block, _ := pem.Decode([]byte(req.CSR))
	if block == nil {
		return false
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.Subject.CommonName != req.NodeID || csr.CheckSignature() != nil {
		return false
	}
	key, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return false
	}
	signature, err := hex.DecodeString(req.RetrySignature)
	if err != nil {
		return false
	}
	digest := enrollmentRetryDigest(req)
	return ecdsa.VerifyASN1(key, digest[:], signature)
}

func requestEnrollment(ctx context.Context, inv Invitation, req enrollmentRequest) (enrollmentResponse, error) {
	var result enrollmentResponse
	u, err := url.Parse(inv.BootstrapURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" {
		return result, errors.New("invitation bootstrap must be an HTTPS origin")
	}
	pin, err := hex.DecodeString(inv.Fingerprint)
	if err != nil || len(pin) != sha256.Size {
		return result, errors.New("invalid invitation CA fingerprint")
	}
	// The invitation pins the root CA out-of-band. Manually verify that root,
	// the entire server chain, hostname and expiry; do not merely pin a leaf.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("missing enrollment certificate")
		}
		roots := x509.NewCertPool()
		intermediates := x509.NewCertPool()
		found := false
		for _, cert := range cs.PeerCertificates {
			sum := sha256.Sum256(cert.Raw)
			if subtle.ConstantTimeCompare(pin, sum[:]) == 1 && cert.IsCA {
				roots.AddCert(cert)
				found = true
			} else {
				intermediates.AddCert(cert)
			}
		}
		if !found {
			return errors.New("invitation CA fingerprint mismatch")
		}
		_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, DNSName: u.Hostname()})
		return err
	}}
	body, _ := json.Marshal(req)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(inv.BootstrapURL, "/")+"/lab/enroll", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: tlsConfig}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("enrollment redirects forbidden") }}
	resp, err := client.Do(request)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return result, fmt.Errorf("enrollment failed: HTTP %d: %s", resp.StatusCode, body)
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result)
	return result, err
}
