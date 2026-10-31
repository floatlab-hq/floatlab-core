// Package lab manages shared membership without owning any workload state.
package lab

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func fingerprint(der []byte) string { sum := sha256.Sum256(der); return hex.EncodeToString(sum[:]) }

func keyAndCSR(nodeID string) ([]byte, []byte, error) {
	if !validID.MatchString(nodeID) {
		return nil, nil, errors.New("invalid node ID")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	csr, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: nodeID}}, key)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), err
}

func newCA(labID string) ([]byte, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "FloatLab " + labID}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), err
}

func signCSR(caPEM, keyPEM, csrPEM []byte, labID, nodeID, peerURL, raftAddress, rqliteAddress string) ([]byte, error) {
	cb, _ := pem.Decode(caPEM)
	kb, _ := pem.Decode(keyPEM)
	rb, _ := pem.Decode(csrPEM)
	if cb == nil || kb == nil || rb == nil {
		return nil, errors.New("invalid signing material")
	}
	ca, err := x509.ParseCertificate(cb.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(kb.Bytes)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(rb.Bytes)
	if err != nil {
		return nil, err
	}
	if err = csr.CheckSignature(); err != nil {
		return nil, err
	}
	if csr.Subject.CommonName != nodeID || !validID.MatchString(nodeID) {
		return nil, errors.New("CSR identity mismatch")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	identity := &url.URL{Scheme: "spiffe", Host: labID, Path: "/node/" + nodeID}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: serial, Subject: csr.Subject, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{identity}}
	u, err := url.Parse(peerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" {
		return nil, errors.New("peer_url must be an HTTPS origin")
	}
	hosts := []string{u.Hostname(), "rqlite", "localhost", "127.0.0.1"}
	for _, addr := range []string{raftAddress, rqliteAddress} {
		host, _, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, fmt.Errorf("invalid advertised address: %w", e)
		}
		hosts = append(hosts, host)
	}
	for _, host := range hosts {
		if ip := net.ParseIP(host); ip != nil {
			cert.IPAddresses = append(cert.IPAddresses, ip)
		} else {
			cert.DNSNames = append(cert.DNSNames, host)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, ca, csr.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (s *Service) installIdentity(ca, cert, key []byte) error {
	if _, err := tls.X509KeyPair(cert, key); err != nil {
		return err
	}
	for name, data := range map[string][]byte{"ca.pem": ca, "node.pem": cert, "node-key.pem": key} {
		if err := update.WriteFile(filepath.Join(s.cfg.DataDir, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

// TLSConfig enforces both certificate verification and the locally cached member
// allowlist. RefreshTrust applies a newer committed revocation to new sessions.
func (s *Service) TLSConfig() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(filepath.Join(s.cfg.DataDir, "node.pem"), filepath.Join(s.cfg.DataDir, "node-key.pem"))
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "ca.pem"))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid lab CA")
	}
	block, _ := pem.Decode(ca)
	cert.Certificate = append(cert.Certificate, block.Bytes)
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: pool, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("missing peer certificate")
		}
		return s.authorizeCertificate(cs.PeerCertificates[0])
	}}, nil
}

// BootstrapTLSConfig permits invitation-bearing clients without a certificate.
// The bootstrap listener must expose only EnrollmentHandler and routes guarded
// by CheckPeer. ClientTLSConfig retains mandatory peer authentication.
func (s *Service) BootstrapTLSConfig() (*tls.Config, error) {
	cfg, err := s.TLSConfig()
	if err != nil {
		return nil, err
	}
	verify := cfg.VerifyConnection
	cfg.ClientAuth = tls.VerifyClientCertIfGiven
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return nil
		}
		return verify(cs)
	}
	return cfg, nil
}

func (s *Service) authorizeCertificate(cert *x509.Certificate) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, uri := range cert.URIs {
		if uri.Scheme == "spiffe" && uri.Host == s.state.LabID {
			for _, m := range s.state.Members {
				if uri.Path == "/node/"+m.NodeID && m.Status == "active" && (m.CertificateFingerprint == fingerprint(cert.Raw) || m.PreviousCertificateFingerprint == fingerprint(cert.Raw)) {
					return nil
				}
			}
		}
	}
	return errors.New("peer identity is not an active lab member")
}

// CheckPeer must run for every request, including requests on existing TLS
// sessions, so learning a tombstone immediately blocks further peer actions.
func (s *Service) CheckPeer(rtls *tls.ConnectionState) error {
	if rtls == nil || len(rtls.PeerCertificates) == 0 {
		return errors.New("mutual TLS required")
	}
	return s.authorizeCertificate(rtls.PeerCertificates[0])
}
