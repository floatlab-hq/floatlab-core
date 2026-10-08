package hostnetwork

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type testBackend struct {
	live     Live
	applied  chan Config
	armErr   error
	applyErr error
	failMode string
	mu       sync.Mutex
	arms     int
	services map[string]Service
}

func (b *testBackend) Live(context.Context) (Live, error) { return b.live, nil }
func (b *testBackend) Apply(_ context.Context, _, c Config, _ string, _ map[string]Service) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.applied != nil {
		b.applied <- c
	}
	if c.IPv4.Mode == b.failMode {
		return b.applyErr
	}
	return nil
}
func (b *testBackend) Arm(context.Context, string, time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.arms++
	return b.armErr
}
func (b *testBackend) Cancel(context.Context, string) error { return nil }
func (b *testBackend) Service(_ context.Context, s Service, add bool) error {
	if b.services == nil {
		b.services = map[string]Service{}
	}
	if add {
		b.services[s.StackID] = s
	} else {
		delete(b.services, s.StackID)
	}
	return nil
}
func (b *testBackend) ArchiveLegacy() error { return nil }
func setupManager(t *testing.T) (*Manager, *testBackend) {
	t.Helper()
	b := &testBackend{live: Live{Adapters: []Adapter{{Name: "eth0", Eligible: true, PermanentMAC: "02:00:00:00:00:01"}}, Addresses: []string{"192.0.2.2/24"}}}
	m := New(filepath.Join(t.TempDir(), "network.toml"), b)
	if err := m.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.applied = make(chan Config, 8)
	return m, b
}
func staticConfig() Config {
	c := DefaultConfig()
	c.IPv4 = IPv4{Mode: "static", Address: "192.0.2.2", Netmask: "255.255.255.0", Gateway: "192.0.2.1"}
	return c
}
func submit(t *testing.T, m *Manager, b *testBackend) Change {
	t.Helper()
	c, err := m.Submit(context.Background(), ApplyPayload{Revision: Revision(DefaultConfig()), Config: staticConfig(), Key: "key", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.applied:
	case <-time.After(3 * time.Second):
		t.Fatal("apply did not run")
	}
	deadline := time.Now().Add(time.Second)
	for {
		c, err = m.Get(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.State != "applying" {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal("apply never settled")
		}
		time.Sleep(time.Millisecond)
	}
}
func TestNetworkValidation(t *testing.T) {
	for name, change := range map[string]func(*Config){"mask": func(c *Config) { c.IPv4.Netmask = "255.0.255.0" }, "gateway": func(c *Config) { c.IPv4.Gateway = "198.51.100.1" }, "network": func(c *Config) { c.IPv4.Address = "192.0.2.0" }, "broadcast": func(c *Config) { c.IPv4.Address = "192.0.2.255" }, "DNS": func(c *Config) { c.DNSServers = []string{"bad"} }, "duplicate MAC": func(c *Config) { c.ExcludedMACs = []string{"02:00:00:00:00:01", "02:00:00:00:00:01"} }, "excluded member": func(c *Config) {
		c.ExcludedMACs = []string{"02:00:00:00:00:01"}
		c.Bonds = []Bond{{Name: "bond0", Mode: "802.3ad", Members: []string{"02:00:00:00:00:01", "02:00:00:00:00:02"}}}
	}} {
		t.Run(name, func(t *testing.T) {
			c := staticConfig()
			change(&c)
			if c.Validate() == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	c := staticConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	data, err := toml.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Config
	if err = toml.Unmarshal(data, &roundtrip); err != nil || Revision(roundtrip) != Revision(c) {
		t.Fatal("TOML round trip failed", err)
	}
}
func TestConfirmPersistsAndIsIdempotent(t *testing.T) {
	m, b := setupManager(t)
	c := submit(t, m, b)
	if c.State != "awaiting_confirmation" || c.Deadline.IsZero() {
		t.Fatalf("change: %+v", c)
	}
	if _, err := m.Confirm(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Confirm(c.ID); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(context.Background())
	if err != nil || status.Config.IPv4.Mode != "static" || status.Change != nil {
		t.Fatal("confirmation did not commit", err)
	}
	data, _ := os.ReadFile(m.Path)
	var config Config
	if err = toml.Unmarshal(data, &config); err != nil || config.IPv4.Mode != "static" {
		t.Fatal("canonical TOML not committed")
	}
	if b.arms != 1 {
		t.Fatal("timer was not armed once")
	}
}
func TestTimeoutRestoresAndCannotConfirm(t *testing.T) {
	m, b := setupManager(t)
	c := submit(t, m, b)
	m.Now = func() time.Time { return c.Deadline }
	if _, err := m.Confirm(c.ID); err == nil {
		t.Fatal("expired change confirmed")
	}
	after, _ := m.Get(c.ID)
	if after.State != "rolled_back" {
		t.Fatal(after)
	}
	status, _ := m.Status(context.Background())
	if status.Config.IPv4.Mode != "dhcp" {
		t.Fatal("defaults not restored")
	}
}
func TestRetryAndCompetingUpdates(t *testing.T) {
	m, b := setupManager(t)
	c := submit(t, m, b)
	retry, err := m.Submit(context.Background(), ApplyPayload{Revision: Revision(DefaultConfig()), Config: staticConfig(), Key: "key", Actor: "admin"})
	if err != nil || retry.ID != c.ID {
		t.Fatal("retry created another change", err)
	}
	for _, p := range []ApplyPayload{{Revision: Revision(DefaultConfig()), Config: DefaultConfig(), Key: "key", Actor: "admin"}, {Revision: Revision(DefaultConfig()), Config: staticConfig(), Key: "other", Actor: "admin"}} {
		if _, err = m.Submit(context.Background(), p); err == nil {
			t.Fatal("conflicting update accepted")
		}
	}
	if _, err = m.Rollback(c.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Submit(context.Background(), ApplyPayload{Revision: "stale", Config: staticConfig(), Key: "third", Actor: "admin"}); err == nil {
		t.Fatal("stale revision accepted")
	}
}
func TestApplyFailureAndStandaloneRecovery(t *testing.T) {
	m, b := setupManager(t)
	b.failMode = "static"
	b.applyErr = errors.New("partial apply")
	c := submit(t, m, b)
	if c.State != "rolled_back" || !strings.Contains(c.Error, "partial apply") {
		t.Fatal(c)
	}
	b.failMode = ""
	c, err := m.Submit(context.Background(), ApplyPayload{Revision: Revision(DefaultConfig()), Config: staticConfig(), Key: "next", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	<-b.applied // drain earlier rollback
	<-b.applied
	standalone := New(m.Path, b)
	standalone.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err = standalone.Rollback(c.ID, true); err != nil {
		t.Fatal(err)
	}
	status, _ := standalone.Status(context.Background())
	if status.Config.IPv4.Mode != "dhcp" {
		t.Fatal("separate manager failed to restore")
	}
}
func TestStartupRecoversUnconfirmedAndInvalidTOML(t *testing.T) {
	m, b := setupManager(t)
	c := submit(t, m, b)
	restarted := New(m.Path, b)
	if err := restarted.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := restarted.Get(c.ID)
	if after.State != "rolled_back" {
		t.Fatal(after)
	}
	if err := os.WriteFile(m.Path, []byte("invalid = ["), 0600); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Initialize(context.Background()); err != nil {
		t.Fatal("confirmed fallback not used", err)
	}
	fresh := New(filepath.Join(t.TempDir(), "network.toml"), b)
	_ = os.WriteFile(fresh.Path, []byte("invalid = ["), 0600)
	if fresh.Initialize(context.Background()) == nil {
		t.Fatal("invalid TOML without fallback accepted")
	}
}
func TestConfirmRollbackRace(t *testing.T) {
	m, b := setupManager(t)
	c := submit(t, m, b)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = m.Confirm(c.ID) }()
	go func() { defer wg.Done(); _, _ = New(m.Path, b).Rollback(c.ID, false) }()
	wg.Wait()
	status, err := m.Status(context.Background())
	if err != nil || status.Change != nil {
		t.Fatal("transaction not settled", err)
	}
	after, _ := m.Get(c.ID)
	if after.State == "confirmed" && status.Config.IPv4.Mode != "static" || after.State == "rolled_back" && status.Config.IPv4.Mode != "dhcp" {
		t.Fatal("commit and rollback disagreed")
	}
}
func TestActiveServiceProtectionAndPrimaryAddresses(t *testing.T) {
	m, b := setupManager(t)
	service := Service{StackID: "stack", PoolID: "pool", Address: "192.0.2.10/24"}
	if err := m.SetService(context.Background(), service, true); err != nil {
		t.Fatal(err)
	}
	b.live.Addresses = append(b.live.Addresses, service.Address)
	status, _ := m.Status(context.Background())
	if len(status.Live.PrimaryAddresses) != 1 {
		t.Fatal("service alias mistaken for primary")
	}
	c := staticConfig()
	c.IPv4.Address = "198.51.100.2"
	c.IPv4.Gateway = "198.51.100.1"
	if _, err := m.Submit(context.Background(), ApplyPayload{Revision: Revision(DefaultConfig()), Config: c, Key: "change", Actor: "admin"}); err == nil {
		t.Fatal("active allocation invalidated")
	}
	if err := m.SetService(context.Background(), Service{StackID: "other", PoolID: "pool", Address: service.Address}, true); err == nil {
		t.Fatal("duplicate ownership accepted")
	}
	if err := m.SetService(context.Background(), service, false); err != nil {
		t.Fatal(err)
	}
}
func TestNetworkdOwnershipConfiguration(t *testing.T) {
	c := DefaultConfig()
	unit := configureDHCP(c)
	for _, want := range []string{"Name=floatlab-lan", "DHCP=ipv4", "KeepConfiguration=static", "UseDNS=no", "ClientIdentifier=mac"} {
		if !strings.Contains(unit, want) {
			t.Fatal("missing", want)
		}
	}
	if strings.Contains(unit, "KeepConfiguration=yes") {
		t.Fatal("lease expiry disabled")
	}
	c.DNSServers = nil
	if !strings.Contains(configureDHCP(c), "UseDNS=yes") {
		t.Fatal("DHCP DNS not enabled without configured nameservers")
	}
}

func TestAdapterClassification(t *testing.T) {
	for _, tc := range []struct {
		kind             string
		device, wireless bool
		flags            net.Flags
		eligible         bool
	}{{"device", true, false, net.FlagUp, true}, {"device", true, true, net.FlagUp, false}, {"device", false, false, net.FlagUp, false}, {"device", true, false, net.FlagLoopback, false}, {"veth", true, false, net.FlagUp, false}, {"bridge", true, false, net.FlagUp, false}, {"bond", true, false, net.FlagUp, false}} {
		if eligibleAdapter(tc.kind, tc.device, tc.wireless, tc.flags) != tc.eligible {
			t.Fatal("adapter incorrectly classified", tc)
		}
	}
}
func TestTimerArmFailureMakesNoChanges(t *testing.T) {
	m, b := setupManager(t)
	b.armErr = errors.New("systemd unavailable")
	c, err := m.Submit(context.Background(), ApplyPayload{Revision: Revision(DefaultConfig()), Config: staticConfig(), Key: "key", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		c, err = m.Get(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.State == "failed" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if c.State != "failed" {
		t.Fatal(c)
	}
	select {
	case <-b.applied:
		t.Fatal("changed networking without watchdog")
	default:
	}
	status, _ := m.Status(context.Background())
	if status.Change != nil {
		t.Fatal("failed arm blocked future changes")
	}
}
