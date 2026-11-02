package installer

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testSSHKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(key))
}

func TestSSHKeyURL(t *testing.T) {
	for _, tc := range []struct{ source, reference, want string }{
		{"", "", ""}, {"none", "unused", ""},
		{"github", "alice", "https://github.com/alice.keys"},
		{"gitlab", "a.b", "https://gitlab.com/a.b.keys"},
		{"launchpad", "alice", "https://launchpad.net/~alice/+sshkeys"},
		{"web", "http://localhost:8080/keys?user=a", "http://localhost:8080/keys?user=a"},
	} {
		got, err := sshKeyURL(tc.source, tc.reference)
		if err != nil || got != tc.want {
			t.Fatalf("%+v: %q %v", tc, got, err)
		}
	}
	for _, tc := range [][2]string{{"github", ""}, {"github", "a/b"}, {"gitlab", "a?b"}, {"launchpad", "a#b"}, {"github", "a\nb"}, {"github", ".."}, {"github", "https://github.com/a"}, {"other", "alice"}, {"web", "file:///tmp/key"}, {"web", "https:///keys"}, {"web", "https://user:password@example.com/keys"}, {"web", "https://example.com/#keys"}} {
		if _, err := sshKeyURL(tc[0], tc[1]); err == nil {
			t.Fatalf("accepted %q", tc)
		}
	}
	config := validConfig()
	config.SSHKeySource, config.SSHKeyReference = "github", "a/b"
	if validateAccount(config) == nil {
		t.Fatal("account validation accepted invalid key reference")
	}
}

func TestParseSSHKeys(t *testing.T) {
	key, other := testSSHKey(t), testSSHKey(t)
	got, err := parseSSHKeys([]byte("# keys\n\n" + strings.TrimSpace(key) + " comment\r\n" + key + other))
	if err != nil || got != key+other {
		t.Fatalf("normalized keys: %q %v", got, err)
	}
	for _, data := range []string{"", "# no keys", "<html>not keys</html>", key + "bad", "command=\"id\" " + key, "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----"} {
		if _, err := parseSSHKeys([]byte(data)); err == nil {
			t.Fatalf("accepted %q", data)
		}
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	sshPub, _ := ssh.NewPublicKey(pub)
	cert := &ssh.Certificate{Key: sshPub, CertType: ssh.UserCert, ValidBefore: ssh.CertTimeInfinity}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		t.Fatal(err)
	}
	if _, err := parseSSHKeys(ssh.MarshalAuthorizedKey(cert)); err == nil {
		t.Fatal("accepted certificate")
	}
	block, _ := ssh.MarshalPrivateKey(priv, "")
	if _, err := parseSSHKeys(pem.EncodeToMemory(block)); err == nil {
		t.Fatal("accepted private key")
	}
}

func TestFetchSSHKeys(t *testing.T) {
	key := testSSHKey(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/keys":
			_, _ = w.Write([]byte(key + key))
		case "/empty":
		case "/bad":
			_, _ = w.Write([]byte("not a key"))
		case "/large":
			_, _ = w.Write([]byte(strings.Repeat(" ", maxSSHKeyBytes+1)))
		case "/error":
			w.WriteHeader(404)
		case "/slow":
			<-r.Context().Done()
		case "/redirect":
			http.Redirect(w, r, "/keys", 302)
		case "/loop":
			http.Redirect(w, r, "/loop", 302)
		}
	}))
	defer server.Close()
	for _, path := range []string{"/keys", "/redirect"} {
		got, err := fetchSSHKeys(context.Background(), "web", server.URL+path)
		if err != nil || got != key {
			t.Fatalf("%s: %q %v", path, got, err)
		}
	}
	for _, path := range []string{"/empty", "/bad", "/large", "/error", "/loop"} {
		if _, err := fetchSSHKeys(context.Background(), "web", server.URL+path); err == nil {
			t.Fatal("accepted", path)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := fetchSSHKeys(ctx, "web", server.URL+"/slow"); err == nil {
		t.Fatal("ignored timeout")
	}
	config := validConfig()
	config.SSHKeySource, config.SSHKeyReference = "web", server.URL+"/keys"
	sub, err := (&Service{identity: Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)}, key: make([]byte, 32)}).submission(context.Background(), config, false)
	if err != nil || sub.SSHAuthorizedKeys != key {
		t.Fatalf("submission: %v %q", err, sub.SSHAuthorizedKeys)
	}
}

func TestSSHKeyRedirect(t *testing.T) {
	request := func(address string) *http.Request { u, _ := url.Parse(address); return &http.Request{URL: u} }
	for _, tc := range []struct {
		target, from string
		count        int
		valid        bool
	}{
		{"https://example.com/keys", "https://example.com/old", 5, true},
		{"https://example.com/keys", "https://example.com/old", 6, false},
		{"http://localhost/keys", "https://example.com/old", 1, false},
		{"file:///keys", "http://localhost/old", 1, false},
		{"https://user:pass@example.com/keys", "http://localhost/old", 1, false},
		{"https://example.com/keys", "http://localhost/old", 1, true},
	} {
		via := make([]*http.Request, tc.count)
		for i := range via {
			via[i] = request(tc.from)
		}
		if err := sshKeyRedirect(request(tc.target), via); (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
