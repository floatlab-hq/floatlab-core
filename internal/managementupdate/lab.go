package managementupdate

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

const labRuntimePath = "/floatlab/system/etc/lab/runtime.json"

func (l *Linux) rqliteRead(ctx context.Context, path string) (string, error) {
	client, origin, err := labRQLiteClient(labRuntimePath)
	if errors.Is(err, os.ErrNotExist) {
		return l.run(ctx, "docker", "exec", l.controlID(ctx), "wget", "-qO-", "http://rqlite:4001"+path)
	}
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+path, nil)
	if err != nil {
		return "", err
	}
	response, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("lab rqlite query failed: HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return strings.TrimSpace(string(data)), err
}

func labRQLiteClient(path string) (*http.Client, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	var runtime struct {
		RQLiteAddress  string `json:"rqlite_address"`
		CAPEM          string `json:"ca_pem"`
		CertificatePEM string `json:"certificate_pem"`
		PrivateKeyPEM  string `json:"private_key_pem"`
	}
	if err = json.Unmarshal(data, &runtime); err != nil {
		return nil, "", err
	}
	if _, _, err = net.SplitHostPort(runtime.RQLiteAddress); err != nil {
		return nil, "", err
	}
	cert, err := tls.X509KeyPair([]byte(runtime.CertificatePEM), []byte(runtime.PrivateKeyPEM))
	if err != nil {
		return nil, "", err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(runtime.CAPEM)) {
		return nil, "", errors.New("invalid lab rqlite trust root")
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, Certificates: []tls.Certificate{cert}}}, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("management compatibility query cannot redirect to a peer")
	}}
	return client, "https://" + runtime.RQLiteAddress, nil
}
