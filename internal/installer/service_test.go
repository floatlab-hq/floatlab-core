package installer

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

type serviceTest struct {
	input  *io.PipeWriter
	events chan Event
	done   chan error
	next   int
}

func TestConsoleSessionClaimIsExclusiveAndRejectsStartedInstallations(t *testing.T) {
	root := t.TempDir()
	s := &Service{runtimeDir: root + "/run", persistentDir: root + "/persistent"}
	lock, err := s.claimSession()
	if err != nil {
		t.Fatal(err)
	}
	if other, err := s.claimSession(); err == nil {
		other.Close()
		t.Fatal("two consoles claimed the same session")
	}
	lock.Close()
	for _, path := range []string{s.runtimeDir + "/operation.json", s.runtimeDir + "/status.json", s.persistentDir + "/install-operation.json"} {
		if err := credentials.WriteBytes(path, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if other, err := s.claimSession(); err == nil {
			other.Close()
			t.Fatal("a stale console claimed an installation that already started")
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		other, err := s.claimSession()
		if err != nil {
			t.Fatal("failed claim retained the session lock:", err)
		}
		other.Close()
	}
}

func mockSession(t *testing.T, scenario string) *serviceTest {
	t.Helper()
	input, writer := io.Pipe()
	output, reader := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	session := &serviceTest{input: writer, events: make(chan Event, 100), done: make(chan error, 1)}
	go func() {
		decoder := json.NewDecoder(output)
		for {
			var e Event
			if decoder.Decode(&e) != nil {
				return
			}
			session.events <- e
		}
	}()
	go func() {
		session.done <- (&Service{Mock: true, Scenario: scenario, StepDelay: time.Millisecond}).Serve(ctx, input, reader)
		reader.Close()
	}()
	t.Cleanup(func() { cancel(); writer.Close(); input.Close(); output.Close(); reader.Close() })
	return session
}
func (s *serviceTest) event(t *testing.T, kind string) Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-s.events:
			if e.Type == kind {
				return e
			}
		case <-deadline:
			t.Fatal("no event", kind)
			return Event{}
		}
	}
}
func (s *serviceTest) request(t *testing.T, req Request) Event {
	t.Helper()
	s.next++
	req.ID = s.next
	if err := json.NewEncoder(s.input).Encode(req); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-s.events:
			if e.ID == req.ID {
				return e
			}
		case <-deadline:
			t.Fatal("request timed out", req.Type)
			return Event{}
		}
	}
}
func validConfig() InstallConfig {
	return InstallConfig{NodeName: "node-a", Timezone: "Australia/Melbourne", Network: hostnetwork.DefaultConfig(), Username: "admin", Password: "test-password", PasswordConfirmation: "test-password"}
}
func confirmMock(t *testing.T, s *serviceTest) Event {
	inventory := s.event(t, "inventory")
	preview := s.request(t, Request{Type: "preview", Command: ZPoolCommand(inventory.Disks[:2])})
	if preview.Type != "preview" || !strings.Contains(preview.Command, "mirror /dev/disk/by-id/mock-a-part2") {
		t.Fatal(preview)
	}
	reply := s.request(t, Request{Type: "confirm", Command: preview.Command, Confirmation: "ERASE ALL DATA"})
	if reply.Type != "confirm" {
		t.Fatal(reply)
	}
	return preview
}
func TestServiceRequiresPreview(t *testing.T) {
	s := mockSession(t, "success")
	s.event(t, "inventory")
	for _, req := range []Request{{Type: "confirm", Confirmation: "ERASE ALL DATA"}} {
		if e := s.request(t, req); e.Type != "error" {
			t.Fatal("unsafe action accepted", e)
		}
	}
}
func TestServiceMockWaitsForValidConfigurationAndReboots(t *testing.T) {
	s := mockSession(t, "rejected-config")
	confirmMock(t, s)
	// Copying is independent of the configuration form.
	for {
		e := s.event(t, "status")
		if e.Status.Phase == "Waiting for host settings and credentials" {
			if e.Status.Progress != 0.9 {
				t.Fatal(e.Status)
			}
			break
		}
	}
	config := validConfig()
	if e := s.request(t, Request{Type: "configure", Config: &config}); e.Type != "error" {
		t.Fatal(e)
	}
	config.PasswordConfirmation = "wrong"
	if e := s.request(t, Request{Type: "configure", Config: &config}); e.Type != "error" {
		t.Fatal("mismatched credentials accepted", e)
	}
	config.PasswordConfirmation = config.Password
	if e := s.request(t, Request{Type: "configure", Config: &config}); e.Type != "configure" {
		t.Fatal(e)
	}
	complete := s.event(t, "complete")
	if !complete.Status.Ready || complete.Status.Progress != 1 || complete.Status.Address == "" {
		t.Fatal(complete)
	}
	if e := s.request(t, Request{Type: "reboot"}); e.Type != "reboot" {
		t.Fatal(e)
	}
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mock reboot did not exit")
	}
}
func TestServiceMockFailuresRemainTerminal(t *testing.T) {
	for _, scenario := range []string{"failure", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			s := mockSession(t, scenario)
			confirmMock(t, s)
			e := s.event(t, "failed")
			if e.Status.Error == "" || e.Status.Ready {
				t.Fatal(e)
			}
			if e := s.request(t, Request{Type: "preview", Command: "zpool create floatlab /dev/sda"}); e.Type != "error" {
				t.Fatal("retry accepted", e)
			}
			if e := s.request(t, Request{Type: "reboot"}); e.Type != "reboot" {
				t.Fatal(e)
			}
		})
	}
}
func TestServiceEOFStopsWorker(t *testing.T) {
	s := mockSession(t, "success")
	confirmMock(t, s)
	s.input.Close()
	select {
	case err := <-s.done:
		if err != io.EOF {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker leaked after frontend EOF")
	}
}
func TestEnrollmentRequiresValidCodeAndSavedRecoveryCodes(t *testing.T) {
	s := &Service{Mock: true, ntp: true, key: make([]byte, 32), identity: Appliance{NodeID: "node", HostID: "12345678", JWTSecret: strings.Repeat("a", 64)}}
	config := validConfig()
	config.TOTP = true
	if _, err := s.submission(context.Background(), config, true); err != nil {
		t.Fatal(err)
	}
	if len(s.enrollment.RecoveryCodes) != 10 {
		t.Fatal("missing recovery codes")
	}
	code, err := credentials.Code(s.enrollment.Secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	config.TOTPCode = code
	if _, err := s.submission(context.Background(), config, false); err == nil {
		t.Fatal("accepted unsaved recovery codes")
	}
	config.RecoverySaved = true
	sub, err := s.submission(context.Background(), config, false)
	if err != nil || sub.Admin.TOTPSecret == s.enrollment.Secret || sub.VerificationPassword == config.Password {
		t.Fatal("credentials not encrypted", err)
	}
}

func TestHostValidationBeforeAccountSetup(t *testing.T) {
	s := mockSession(t, "success")
	confirmMock(t, s)
	config := InstallConfig{NodeName: "node-a", Network: hostnetwork.DefaultConfig()}
	if e := s.request(t, Request{Type: "validate-host", Config: &config}); e.Type != "error" || !strings.Contains(e.Error, "select a timezone") {
		t.Fatal("missing timezone did not require selection", e)
	}
	config.Timezone = "UTC"
	config.Network.IPv4 = hostnetwork.IPv4{Mode: "static", Address: "192.0.2.20", Netmask: "255.255.255.0", Gateway: "198.51.100.1"}
	if e := s.request(t, Request{Type: "validate-host", Config: &config}); e.Type != "error" {
		t.Fatal("invalid gateway accepted", e)
	}
	config.Network.IPv4.Gateway = "192.0.2.1"
	if e := s.request(t, Request{Type: "validate-host", Config: &config}); e.Type != "validate-host" {
		t.Fatal("valid network rejected", e)
	}
	// Host validation neither requires account credentials nor starts finalization.
	if e := s.request(t, Request{Type: "validate-host", Config: &config}); e.Type != "validate-host" {
		t.Fatal("host validation finalized the installation", e)
	}
}

func TestAccountAndAuthenticatorValidateBeforeReview(t *testing.T) {
	s := mockSession(t, "success")
	confirmMock(t, s)
	for _, edit := range []func(*InstallConfig){
		func(c *InstallConfig) { c.Username = "" },
		func(c *InstallConfig) { c.Password = "" },
		func(c *InstallConfig) { c.PasswordConfirmation = "" },
		func(c *InstallConfig) { c.PasswordConfirmation = "different" },
		func(c *InstallConfig) {
			c.Password, c.PasswordConfirmation = strings.Repeat("a", 73), strings.Repeat("a", 73)
		},
	} {
		config := validConfig()
		edit(&config)
		if e := s.request(t, Request{Type: "validate-account", Config: &config}); e.Type != "error" {
			t.Fatal("invalid account passed validation", e)
		}
	}
	config := validConfig()
	if e := s.request(t, Request{Type: "validate-account", Config: &config}); e.Type != "validate-account" {
		t.Fatal("valid account rejected", e)
	}
	config.TOTP = true
	enrollment := s.request(t, Request{Type: "enroll", Config: &config})
	if enrollment.Type != "enroll" || enrollment.Enrollment == nil {
		t.Fatal("could not enroll authenticator", enrollment)
	}
	config.TOTPCode = "invalid"
	if e := s.request(t, Request{Type: "verify-totp", Config: &config}); e.Type != "error" {
		t.Fatal("invalid authenticator code accepted", e)
	}
	code, err := credentials.Code(enrollment.Enrollment.Secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	config.TOTPCode = code
	if e := s.request(t, Request{Type: "verify-totp", Config: &config}); e.Type != "verify-totp" {
		t.Fatal("valid authenticator code rejected", e)
	}
	config.RecoverySaved = true
	if e := s.request(t, Request{Type: "configure", Config: &config}); e.Type != "configure" {
		t.Fatal("verified configuration rejected", e)
	}
}

func TestServiceRebootsBeforeAndDuringInstallation(t *testing.T) {
	for _, active := range []bool{false, true} {
		s := mockSession(t, "success")
		if active {
			confirmMock(t, s)
		} else {
			s.event(t, "inventory")
		}
		if event := s.request(t, Request{Type: "reboot"}); event.Type != "reboot" {
			t.Fatal("reboot was rejected", event)
		}
		select {
		case err := <-s.done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("reboot did not stop the worker and close the service")
		}
	}
}
