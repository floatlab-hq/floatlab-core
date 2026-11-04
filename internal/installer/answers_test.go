package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func testAnswers(t *testing.T) Answers {
	t.Helper()
	answers, err := NewAnswers(ZPoolCommand([]Disk{{Path: "/dev/disk/by-id/mock-a"}}), validConfig())
	if err != nil {
		t.Fatal(err)
	}
	return answers
}

func TestAnswersTOMLRoundTripUsesOnlyPrivateBcryptHashes(t *testing.T) {
	answers := testAnswers(t)
	path := filepath.Join(t.TempDir(), "answers.toml")
	if err := WriteAnswers(path, answers); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("answers permissions: %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("[config.network.ipv4]")) || bytes.Contains(data, []byte(validConfig().Password)) || bytes.Contains(data, []byte(validConfig().RecoveryPassword)) {
		t.Fatal("export must contain TOML settings and no plaintext passwords")
	}
	loaded, err := ReadAnswers(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Command != answers.Command || loaded.Config.Network.IPv4.Mode != "dhcp" || loaded.Config.Password != "" || loaded.Config.RecoveryPassword != "" {
		t.Fatal("answers did not preserve the installation settings")
	}
	for _, pair := range [][2]string{{loaded.Config.PasswordHash, validConfig().Password}, {loaded.Config.RecoveryPasswordHash, validConfig().RecoveryPassword}} {
		if bcrypt.CompareHashAndPassword([]byte(pair[0]), []byte(pair[1])) != nil {
			t.Fatal("exported bcrypt hash does not match the entered password")
		}
	}
	s := &Service{key: make([]byte, 32), identity: Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)}}
	sub, err := s.submission(context.Background(), loaded.Config, false)
	if err != nil || sub.Admin.PasswordHash != loaded.Config.PasswordHash || sub.RecoveryPasswordHash != loaded.Config.RecoveryPasswordHash || sub.VerificationPassword != "" {
		t.Fatal("unattended submission rehashed or retained a plaintext credential", err)
	}
}

func TestAnswersRejectInvalidInputBeforeStartingInstaller(t *testing.T) {
	base := testAnswers(t)
	for name, edit := range map[string]func(*Answers){
		"version":      func(a *Answers) { a.Version = 2 },
		"confirmation": func(a *Answers) { a.Confirmation = "yes" },
		"no disks":     func(a *Answers) { a.Command = "" },
		"plaintext":    func(a *Answers) { a.Config.Password = "secret" },
		"missing hash": func(a *Answers) { a.Config.RecoveryPasswordHash = "" },
		"bad hash":     func(a *Answers) { a.Config.PasswordHash = "not-a-hash" },
		"hostname":     func(a *Answers) { a.Config.NodeName = "bad/name" },
		"timezone":     func(a *Answers) { a.Config.Timezone = "not/a/timezone" },
		"network":      func(a *Answers) { a.Config.Network.Version = 999 },
		"totp":         func(a *Answers) { a.Config.TOTP = true },
		"lab DHCP":     func(a *Answers) { a.Config.EnableLab = true },
		"SSH keys":     func(a *Answers) { a.SSH.PublicKeys = []string{"not a public key"} },
		"SSH source":   func(a *Answers) { a.SSH.Source, a.SSH.Reference = "github", "bad/name" },
	} {
		t.Run(name, func(t *testing.T) {
			answers := base
			edit(&answers)
			var output bytes.Buffer
			s := &Service{Mock: true}
			if err := s.InstallAnswers(context.Background(), answers, false, &output); err == nil || output.Len() != 0 {
				t.Fatal("invalid answers reached the installation state machine")
			}
		})
	}
	for _, input := range []string{"password = 'DO-NOT-LOG'", "[config]\npassword = 'DO-NOT-LOG'", "version=1\nversion=1", strings.Repeat(" ", 1<<20+1)} {
		if _, err := ReadAnswers(strings.NewReader(input)); err == nil || strings.Contains(err.Error(), "DO-NOT-LOG") {
			t.Fatal("unsafe TOML accepted or its contents leaked into diagnostics")
		}
	}
}

func TestAnswersPreserveSSHPublicKeys(t *testing.T) {
	key, other := testSSHKey(t), testSSHKey(t)
	config := validConfig()
	config.SSH.PublicKeys = []string{strings.TrimSpace(key), strings.TrimSpace(other), strings.TrimSpace(key)}
	answers, err := NewAnswers(ZPoolCommand([]Disk{{Path: "/dev/disk/by-id/mock-a"}}), config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "answers.toml")
	if err := WriteAnswers(path, answers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("[ssh]")) || !bytes.Contains(data, []byte("public_keys")) || bytes.Contains(data, []byte("ssh_public_keys")) {
		t.Fatal("answer file is missing SSH public keys")
	}
	loaded, err := ReadAnswers(bytes.NewReader(data))
	if err != nil || len(loaded.SSH.PublicKeys) != 3 {
		t.Fatal("answer file lost SSH keys", err)
	}
	s := &Service{identity: Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)}}
	sub, err := s.submission(context.Background(), loaded.installConfig(), false)
	if err != nil || sub.SSHAuthorizedKeys != key+other {
		t.Fatalf("pre-hashed answer submission lost or duplicated SSH keys: %q %v", sub.SSHAuthorizedKeys, err)
	}
}

func TestAnswersSSHSourceReachesUnattendedInstallation(t *testing.T) {
	key := testSSHKey(t)
	fetched := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched <- struct{}{}
		_, _ = io.WriteString(w, key)
	}))
	defer server.Close()
	config := validConfig()
	config.SSH.Source, config.SSH.Reference = "web", server.URL
	answers, err := NewAnswers(ZPoolCommand([]Disk{{Path: "/dev/disk/by-id/mock-a"}}), config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "answers.toml")
	if err := WriteAnswers(path, answers); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadAnswers(bytes.NewReader(data))
	if err != nil || loaded.SSH.Source != "web" || loaded.SSH.Reference != server.URL {
		t.Fatal("answer file lost SSH source", err)
	}
	if len(fetched) != 0 {
		t.Fatal("export or validation fetched keys")
	}
	s := &Service{Mock: true, StepDelay: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.InstallAnswers(ctx, loaded, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(fetched) != 1 {
		t.Fatal("unattended configuration did not fetch the SSH source")
	}
}

func TestUnattendedUsesInstallerSessionAndRebootsOnlyAfterSuccess(t *testing.T) {
	base := testAnswers(t)
	for _, scenario := range []string{"success", "failure", "rejected-config", "invalid-disk"} {
		t.Run(scenario, func(t *testing.T) {
			answers := base
			s := &Service{Mock: true, Scenario: scenario, StepDelay: time.Millisecond}
			if scenario == "invalid-disk" {
				s.Scenario = "success"
				answers.Command = "zpool create floatlab /dev/disk/by-id/not-present"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var output bytes.Buffer
			err := s.InstallAnswers(ctx, answers, true, &output)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("scenario %s: %v", scenario, err)
			}
			seen := map[string]bool{}
			decoder := json.NewDecoder(&output)
			for {
				var event Event
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if event.Type == "reboot" && !seen["complete"] {
					t.Fatal("unattended installer rebooted before successful verification")
				}
				seen[event.Type] = true
			}
			if scenario == "success" && (!seen["preview"] || !seen["confirm"] || !seen["configure"] || !seen["complete"] || !seen["reboot"]) {
				t.Fatal("unattended path bypassed the interactive installer session")
			}
			if scenario != "success" && seen["reboot"] {
				t.Fatal("failed installation rebooted")
			}
		})
	}
}

func TestUnattendedCancellationStopsWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Service{Mock: true}).InstallAnswers(ctx, testAnswers(t), false, io.Discard); err == nil {
		t.Fatal("cancelled installation succeeded")
	}
}

func TestLabAnswersGenerateAdvertisedAddressesFromInstalledNetwork(t *testing.T) {
	a := Appliance{NodeID: "generated-identity", EnableLab: true, Network: validConfig().Network}
	a.Network.IPv4.Mode, a.Network.IPv4.Address = "static", "192.0.2.20"
	want := "FLOATLAB_NODE_ID=generated-identity\nFLOATLAB_PEER_URL=https://192.0.2.20:9443\nFLOATLAB_RQLITE_RAFT_ADDRESS=192.0.2.20:4002\nFLOATLAB_RQLITE_ADDRESS=192.0.2.20:4001\n"
	if applianceEnvironment(a) != want {
		t.Fatal("installed lab runtime did not use the confirmed network")
	}
}
