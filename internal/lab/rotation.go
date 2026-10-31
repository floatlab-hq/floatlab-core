package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/floatlab/floatlab-core/internal/update"
)

type RotationRequest struct {
	OperationID string `json:"operation_id"`
}
type RotationStatus struct {
	OperationID    string   `json:"operation_id"`
	Phase          string   `json:"phase"`
	TrustRevision  int64    `json:"trust_revision"`
	PendingMembers []string `json:"pending_members"`
}
type rotationJournal struct {
	Status           RotationStatus `json:"status"`
	CertificatePEM   string         `json:"certificate_pem,omitempty"`
	PrivateKeyPEM    string         `json:"private_key_pem,omitempty"`
	SSHPrivateKeyPEM string         `json:"ssh_private_key_pem,omitempty"`
	SSHPublicKey     string         `json:"ssh_public_key,omitempty"`
	OldMember        Member         `json:"old_member"`
}

// RotateIdentity keeps both old/new credentials authorized until all enrolled
// members report applying the new trust revision. Retry the same operation to
// complete revocation; partitions cannot silently turn this into success.
func (s *Service) RotateIdentity(ctx context.Context, req RotationRequest) (RotationStatus, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if !validID.MatchString(req.OperationID) || s.cfg.Rotate == nil {
		return RotationStatus{}, errors.New("operation ID and credential-rotation runtime required")
	}
	if err := s.RefreshTrust(ctx); err != nil {
		return RotationStatus{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	path := filepath.Join(s.cfg.DataDir, "rotation-"+req.OperationID+".json")
	var journal rotationJournal
	if data, err := os.ReadFile(path); err == nil {
		if err = json.Unmarshal(data, &journal); err != nil {
			return journal.Status, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return journal.Status, err
	} else {
		pending, _ := filepath.Glob(filepath.Join(s.cfg.DataDir, "rotation-*.json"))
		for _, p := range pending {
			data, e := os.ReadFile(p)
			if e != nil {
				return journal.Status, e
			}
			var other rotationJournal
			if e = json.Unmarshal(data, &other); e != nil {
				return journal.Status, e
			}
			if other.Status.Phase != "complete" {
				return other.Status, errors.New("resume existing credential rotation")
			}
		}
		for _, m := range s.Snapshot().Members {
			if m.NodeID == s.cfg.NodeID {
				journal.OldMember = m
			}
		}
		if journal.OldMember.Status != "active" {
			return journal.Status, errors.New("only an active member may rotate credentials")
		}
		key, csr, err := keyAndCSR(s.cfg.NodeID)
		if err != nil {
			return journal.Status, err
		}
		ca, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca.pem"))
		if err != nil {
			return journal.Status, err
		}
		caKey, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca-key.pem"))
		if err != nil {
			return journal.Status, err
		}
		cert, err := signCSR(ca, caKey, csr, s.Snapshot().LabID, s.cfg.NodeID, s.cfg.PeerURL, s.cfg.RaftAddress, s.cfg.RQLiteAddress)
		if err != nil {
			return journal.Status, err
		}
		keyPath, pub, err := EnsureSSHIdentity(filepath.Join(s.cfg.DataDir, "rotation-keys-"+req.OperationID))
		if err != nil {
			return journal.Status, err
		}
		sshKey, err := os.ReadFile(keyPath)
		if err != nil {
			return journal.Status, err
		}
		journal.CertificatePEM = string(cert)
		journal.PrivateKeyPEM = string(key)
		journal.SSHPrivateKeyPEM = string(sshKey)
		journal.SSHPublicKey = pub
		journal.Status = RotationStatus{OperationID: req.OperationID, Phase: "prepared", PendingMembers: []string{}}
		if err = update.WriteJSON(path, journal); err != nil {
			return journal.Status, err
		}
	}
	if journal.Status.Phase == "complete" {
		return journal.Status, nil
	}
	if journal.Status.Phase == "prepared" {
		member := journal.OldMember
		member.PreviousCertificateFingerprint = member.CertificateFingerprint
		member.CertificateFingerprint = fingerprintPEM([]byte(journal.CertificatePEM))
		member.PreviousSSHReplicationKey = member.SSHReplicationKey
		member.SSHReplicationKey = journal.SSHPublicKey
		if err := s.putMember(ctx, member); err != nil {
			return journal.Status, err
		}
		if err := s.RefreshTrust(ctx); err != nil {
			return journal.Status, err
		}
		journal.Status.TrustRevision = s.Snapshot().TrustRevision
		journal.Status.Phase = "authorized"
		if err := update.WriteJSON(path, journal); err != nil {
			return journal.Status, err
		}
	}
	if journal.Status.Phase == "authorized" {
		ca, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca.pem"))
		if err != nil {
			return journal.Status, err
		}
		if err = s.installIdentity(ca, []byte(journal.CertificatePEM), []byte(journal.PrivateKeyPEM)); err != nil {
			return journal.Status, err
		}
		if err = update.WriteFile(filepath.Join(s.cfg.DataDir, "replication-key"), []byte(journal.SSHPrivateKeyPEM), 0600); err != nil {
			return journal.Status, err
		}
		bundle, err := s.InitialBundle()
		if err != nil {
			return journal.Status, err
		}
		if err = s.cfg.Rotate(ctx, bundle); err != nil {
			return journal.Status, err
		}
		s.cfg.SSHReplicationKey = journal.SSHPublicKey
		journal.Status.Phase = "awaiting-trust"
		if err = update.WriteJSON(path, journal); err != nil {
			return journal.Status, err
		}
	}
	if err := s.RefreshTrust(ctx); err != nil {
		return journal.Status, err
	}
	journal.Status.PendingMembers = nil
	for _, m := range s.Snapshot().Members {
		if m.Status == "active" && m.AppliedTrustRevision < journal.Status.TrustRevision {
			journal.Status.PendingMembers = append(journal.Status.PendingMembers, m.NodeID)
		}
	}
	if len(journal.Status.PendingMembers) > 0 {
		return journal.Status, update.WriteJSON(path, journal)
	}
	for _, m := range s.Snapshot().Members {
		if m.NodeID == s.cfg.NodeID {
			m.PreviousCertificateFingerprint = ""
			m.PreviousSSHReplicationKey = ""
			if err := s.putMember(ctx, m); err != nil {
				return journal.Status, err
			}
		}
	}
	if err := s.RefreshTrust(ctx); err != nil {
		return journal.Status, err
	}
	journal.Status.TrustRevision = s.Snapshot().TrustRevision
	journal.Status.Phase = "complete"
	journal.CertificatePEM = ""
	journal.PrivateKeyPEM = ""
	journal.SSHPrivateKeyPEM = ""
	journal.OldMember = Member{}
	if err := update.WriteJSON(path, journal); err != nil {
		return journal.Status, err
	}
	if err := os.RemoveAll(filepath.Join(s.cfg.DataDir, "rotation-keys-"+req.OperationID)); err != nil {
		return journal.Status, err
	}
	return journal.Status, nil
}
