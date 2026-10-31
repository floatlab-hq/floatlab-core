package managementupdate

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagementChecksUseAuthenticatedLabRQLite(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			t.Error("compatibility request missing client identity")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"8.26.0"}`))
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert}
	server.StartTLS()
	defer server.Close()
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	runtime := map[string]string{"rqlite_address": strings.TrimPrefix(server.URL, "https://"), "ca_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})), "certificate_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})), "private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))}
	path := filepath.Join(t.TempDir(), "runtime.json")
	data, _ := json.Marshal(runtime)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	client, origin, err := labRQLiteClient(path)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(origin + "/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.StatusCode)
	}
	runtime["ca_pem"] = "invalid CA"
	data, _ = json.Marshal(runtime)
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = labRQLiteClient(path); err == nil {
		t.Fatal("bad TLS identity must not fall back to anonymous HTTP")
	}
}
