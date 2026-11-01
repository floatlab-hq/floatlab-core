package lab

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/floatlab/floatlab-core/internal/update"
	"golang.org/x/crypto/ssh"
)

func validateSSHKey(key string) error {
	_, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return err
	}
	if len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		return errors.New("SSH key options and multiple keys forbidden")
	}
	return nil
}

// EnsureSSHIdentity creates a dedicated publisher key, never a root/admin key.
// HostKey must be read from the destination sshd host key, not this key.
func EnsureSSHIdentity(dir string) (privatePath, publicKey string, err error) {
	privatePath = filepath.Join(dir, "replication-key")
	data, err := os.ReadFile(privatePath)
	if errors.Is(err, os.ErrNotExist) {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return "", "", e
		}
		block, e := ssh.MarshalPrivateKey(key, "FloatLab workload replication")
		if e != nil {
			return "", "", e
		}
		data = pem.EncodeToMemory(block)
		if err = update.WriteFile(privatePath, data, 0600); err != nil {
			return "", "", err
		}
	} else if err != nil {
		return "", "", err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return "", "", err
	}
	return privatePath, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
}

// ApplySSHTrust writes a dedicated account's complete allowlist. The fixed
// receiver validates SSH_ORIGINAL_COMMAND; options disable all other SSH uses.
func ApplySSHTrust(members []Member, authorizedPath, knownHostsPath, receiverPath string) error {
	if !filepath.IsAbs(receiverPath) || strings.ContainsAny(receiverPath, "\"\n\r '`") {
		return errors.New("receiver must be a fixed absolute executable path")
	}
	var authorized, known strings.Builder
	for _, m := range members {
		if m.Status != "active" && m.Status != "enrolling" {
			continue
		}
		if !validID.MatchString(m.NodeID) {
			return errors.New("invalid member identity")
		}
		for _, key := range []string{m.SSHReplicationKey, m.PreviousSSHReplicationKey} {
			if key != "" {
				if err := validateSSHKey(key); err != nil {
					return err
				}
				fmt.Fprintf(&authorized, "restrict,command=\"%s %s\" %s\n", receiverPath, m.NodeID, key)
			}
		}
		if m.SSHHostKey != "" {
			if err := validateSSHKey(m.SSHHostKey); err != nil {
				return err
			}
			u, err := url.Parse(m.PeerURL)
			if err != nil || u.Hostname() == "" {
				return errors.New("invalid member SSH host")
			}
			fmt.Fprintf(&known, "%s %s\n", u.Hostname(), m.SSHHostKey)
			fmt.Fprintf(&known, "%s %s\n", m.NodeID, m.SSHHostKey)
		}
	}
	if err := update.WriteFile(authorizedPath, []byte(authorized.String()), 0600); err != nil {
		return err
	}
	return update.WriteFile(knownHostsPath, []byte(known.String()), 0600)
}
