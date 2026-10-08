package hostnetwork

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

type Backend interface {
	Live(context.Context) (Live, error)
	Apply(context.Context, Config, Config, string, map[string]Service) error
	Arm(context.Context, string, time.Time) error
	Cancel(context.Context, string) error
	Service(context.Context, Service, bool) error
	ArchiveLegacy() error
}
type Change struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	Deadline  time.Time `json:"deadline"`
	Error     string    `json:"error,omitempty"`
	Candidate Config    `json:"candidate"`
	Previous  Config    `json:"previous"`
}

// DiskChange includes retry identity in the journal without exposing it over HTTP.
type diskChange struct {
	Change
	Key         string `json:"key"`
	Actor       string `json:"actor"`
	RequestHash string `json:"request_hash"`
}
type diskState struct {
	HasConfirmed bool                  `json:"has_confirmed"`
	Startup      *Config               `json:"startup,omitempty"`
	BootID       string                `json:"boot_id"`
	Confirmed    Config                `json:"confirmed"`
	BridgeMAC    string                `json:"bridge_mac"`
	Services     map[string]Service    `json:"services"`
	Pending      string                `json:"pending,omitempty"`
	NeedsConfig  bool                  `json:"needs_config,omitempty"`
	Changes      map[string]diskChange `json:"changes"`
}
type Status struct {
	Config   Config  `json:"config"`
	Revision string  `json:"revision"`
	Live     Live    `json:"live"`
	Change   *Change `json:"change,omitempty"`
}
type Manager struct {
	Path    string
	Backend Backend
	Now     func() time.Time
}

func New(path string, backend Backend) *Manager {
	return &Manager{Path: path, Backend: backend, Now: time.Now}
}
func (m *Manager) dir() string { return m.Path + ".state" }
func (m *Manager) locked(fn func(*diskState) error) error {
	if err := os.MkdirAll(m.dir(), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.dir(), "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	state := diskState{Confirmed: DefaultConfig(), Services: map[string]Service{}, Changes: map[string]diskChange{}}
	data, err := os.ReadFile(filepath.Join(m.dir(), "journal.json"))
	if err == nil {
		if err = json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("network journal: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if state.Services == nil {
		state.Services = map[string]Service{}
	}
	if state.Changes == nil {
		state.Changes = map[string]diskChange{}
	}
	return fn(&state)
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".network-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (m *Manager) save(s *diskState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(m.dir(), "journal.json"), data)
}
func (m *Manager) writeConfig(c Config) error {
	data, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWrite(m.Path, data)
}
func (m *Manager) Initialize(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return m.locked(func(s *diskState) error {
		c := DefaultConfig()
		data, err := os.ReadFile(m.Path)
		if err == nil {
			err = toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&c)
			if err == nil {
				err = c.Validate()
			}
			if err != nil {
				if !s.HasConfirmed {
					return fmt.Errorf("invalid network.toml without confirmed fallback: %w", err)
				}
				c = s.Confirmed
			}
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			if !s.HasConfirmed {
				return err
			}
		}
		if errors.Is(err, os.ErrNotExist) {
			if err = m.Backend.ArchiveLegacy(); err != nil {
				return err
			}
		}
		if s.NeedsConfig {
			c = s.Confirmed
			s.NeedsConfig = false
		}
		bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return err
		}
		if s.BootID != string(bootID) {
			s.Services = map[string]Service{}
			s.BootID = string(bootID)
		}
		previous := s.Confirmed
		if s.Startup != nil {
			previous = *s.Startup
			if s.HasConfirmed {
				c = s.Confirmed
			}
		}
		recovering := s.Pending
		if s.Pending != "" {
			change := s.Changes[s.Pending]
			previous = change.Candidate
			c = s.Confirmed
			// Keep the pending journal until kernel restoration succeeds.
		}
		if err = c.Validate(); err != nil {
			return err
		}
		live, err := m.Backend.Live(ctx)
		if err != nil {
			return err
		}
		if s.BridgeMAC == "" {
			for _, a := range live.Adapters {
				if a.Eligible && !contains(c.ExcludedMACs, a.PermanentMAC) {
					s.BridgeMAC = a.PermanentMAC
					break
				}
			}
			if s.BridgeMAC == "" {
				return fmt.Errorf("no eligible Ethernet adapter for LAN bridge")
			}
		}
		// Save ownership before touching the kernel, including first-boot defaults.
		if err = validateServices(c, s.Services); err != nil {
			return err
		}
		s.Startup = &c
		if err = m.save(s); err != nil {
			return err
		}
		if err = m.Backend.Apply(ctx, previous, c, s.BridgeMAC, s.Services); err != nil {
			return err
		}
		if c.IPv4.Mode == "dhcp" {
			if err = m.verifyServices(ctx, s.Services); err != nil {
				return err
			}
		}
		if recovering != "" {
			change := s.Changes[recovering]
			change.State = "rolled_back"
			change.Error = "unconfirmed change recovered on startup"
			s.Changes[recovering] = change
			s.Pending = ""
		}
		s.Confirmed = c
		s.HasConfirmed = true
		s.Startup = nil
		s.NeedsConfig = true
		if err = m.save(s); err != nil {
			return err
		}
		if err = m.writeConfig(c); err != nil {
			return err
		}
		s.NeedsConfig = false
		return m.save(s)
	})
}

// Atomic journal replacement makes reads consistent without waiting for a long
// topology apply to release its exclusive mutation lock.
func (m *Manager) snapshot() (diskState, error) {
	var s diskState
	data, err := os.ReadFile(filepath.Join(m.dir(), "journal.json"))
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(data, &s)
	return s, err
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	s, err := m.snapshot()
	if err != nil {
		return Status{}, err
	}
	result := Status{Config: s.Confirmed, Revision: Revision(s.Confirmed)}
	if s.Pending != "" {
		c := s.Changes[s.Pending].Change
		result.Change = &c
	}
	result.Live, err = m.Backend.Live(ctx)
	result.Live.PrimaryAddresses = primaryAddresses(result.Live.Addresses, s.Services)
	return result, err
}
func (m *Manager) Get(id string) (Change, error) {
	s, err := m.snapshot()
	if err != nil {
		return Change{}, err
	}
	c, ok := s.Changes[id]
	if !ok {
		return Change{}, Error("not_found", "network change not found")
	}
	return c.Change, nil
}
func validateServices(c Config, services map[string]Service) error {
	if c.IPv4.Mode != "static" {
		return nil
	}
	prefix, _ := c.IPv4.Prefix()
	for _, service := range services {
		p, err := parseService(service)
		if err != nil {
			return err
		}
		if prefix.Masked() != p.Masked() || prefix.Addr() == p.Addr() || c.IPv4.Gateway == p.Addr().String() {
			return Error("conflict", "configuration would invalidate active service addresses")
		}
	}
	return nil
}
func (m *Manager) Submit(ctx context.Context, p ApplyPayload) (Change, error) {
	var result Change
	created := false
	err := m.locked(func(s *diskState) error {
		if p.Key == "" || len(p.Key) > 255 || p.Actor == "" {
			return Error("invalid", "idempotency key and authenticated actor required")
		}
		if err := p.Config.Validate(); err != nil {
			return err
		}
		hash := p.Revision + ":" + Revision(p.Config)
		for _, c := range s.Changes {
			if c.Key == p.Key && c.Actor == p.Actor {
				if c.RequestHash != hash {
					return Error("conflict", "idempotency key already used for another request")
				}
				result = c.Change
				return nil
			}
		}
		if s.Pending != "" {
			return Error("conflict", "a network change is outstanding")
		}
		if p.Revision != Revision(s.Confirmed) {
			return Error("conflict", "stale network revision")
		}
		if err := validateServices(p.Config, s.Services); err != nil {
			return err
		}
		live, err := m.Backend.Live(ctx)
		if err != nil {
			return err
		}
		eligible := map[string]bool{}
		for _, a := range live.Adapters {
			if a.Eligible {
				eligible[a.PermanentMAC] = true
			}
		}
		for _, b := range p.Config.Bonds {
			for _, mac := range b.Members {
				if !eligible[mac] {
					return Error("invalid", "bond member is not an available Ethernet adapter")
				}
			}
		}
		count := 0
		for mac := range eligible {
			if !contains(p.Config.ExcludedMACs, mac) {
				count++
			}
		}
		if count == 0 {
			return Error("invalid", "configuration must retain an Ethernet uplink")
		}
		result = Change{ID: uuid.NewString(), State: "applying", Candidate: p.Config, Previous: s.Confirmed}
		s.Pending = result.ID
		s.Changes[result.ID] = diskChange{Change: result, Key: p.Key, Actor: p.Actor, RequestHash: hash}
		created = true
		return m.save(s)
	})
	if err != nil {
		return Change{}, err
	}
	if created {
		go m.apply(result.ID)
	}
	return result, nil
}
func (m *Manager) apply(id string) {
	_ = m.locked(func(s *diskState) error {
		if s.Pending != id {
			return nil
		}
		c := s.Changes[id]
		c.Deadline = m.Now().UTC().Add(60 * time.Second).Truncate(time.Microsecond)
		s.Changes[id] = c
		if err := m.save(s); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := m.Backend.Arm(ctx, id, c.Deadline); err != nil {
			c.State = "failed"
			c.Error = "cannot arm rollback: " + err.Error()
			s.Changes[id] = c
			s.Pending = ""
			return m.save(s)
		}
		err := m.Backend.Apply(ctx, c.Previous, c.Candidate, s.BridgeMAC, s.Services)
		if err == nil && c.Candidate.IPv4.Mode == "dhcp" {
			err = m.verifyServices(ctx, s.Services)
		}
		if err != nil {
			c.Error = err.Error()
			s.Changes[id] = c
			return m.restore(s, id, "apply failed")
		}
		if !m.Now().Before(c.Deadline) {
			return m.restore(s, id, "confirmation deadline expired during apply")
		}
		c.State = "awaiting_confirmation"
		s.Changes[id] = c
		return m.save(s)
	})
}
func (m *Manager) restore(s *diskState, id, reason string) error {
	c := s.Changes[id]
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := m.Backend.Apply(ctx, c.Candidate, c.Previous, s.BridgeMAC, s.Services)
	if err == nil && c.Previous.IPv4.Mode == "dhcp" {
		err = m.verifyServices(ctx, s.Services)
	}
	if err != nil {
		c.State = "failed"
		c.Error = reason + ": rollback failed: " + err.Error()
		s.Changes[id] = c
		_ = m.save(s)
		return err
	}
	if err = m.writeConfig(c.Previous); err != nil {
		return err
	}
	s.Confirmed = c.Previous
	c.State = "rolled_back"
	if c.Error == "" {
		c.Error = reason
	}
	s.Changes[id] = c
	s.Pending = ""
	if err = m.save(s); err != nil {
		return err
	}
	_ = m.Backend.Cancel(ctx, id)
	return nil
}
func (m *Manager) Confirm(id string) (Change, error) {
	err := m.locked(func(s *diskState) error {
		c, ok := s.Changes[id]
		if !ok {
			return Error("not_found", "network change not found")
		}
		if c.State == "confirmed" {
			return nil
		}
		if s.Pending != id || c.State != "awaiting_confirmation" {
			return Error("conflict", "change cannot be confirmed")
		}
		if !m.Now().Before(c.Deadline) {
			if err := m.restore(s, id, "confirmation deadline expired"); err != nil {
				return err
			}
			return Error("conflict", "confirmation deadline expired")
		}
		// The durable journal is the commit record; startup repairs the canonical file.
		s.Confirmed = c.Candidate
		s.NeedsConfig = true
		c.State = "confirmed"
		s.Changes[id] = c
		s.Pending = ""
		if err := m.save(s); err != nil {
			return err
		}
		if err := m.writeConfig(c.Candidate); err != nil {
			return err
		}
		s.NeedsConfig = false
		if err := m.save(s); err != nil {
			return err
		}
		_ = m.Backend.Cancel(context.Background(), id)
		return nil
	})
	if err != nil {
		return Change{}, err
	}
	return m.Get(id)
}
func (m *Manager) Rollback(id string, expiredOnly bool) (Change, error) {
	err := m.locked(func(s *diskState) error {
		c, ok := s.Changes[id]
		if !ok {
			return Error("not_found", "network change not found")
		}
		if s.Pending != id {
			if c.State == "rolled_back" || c.State == "confirmed" {
				return nil
			}
			return Error("conflict", "change is not pending")
		}
		if expiredOnly && m.Now().Before(c.Deadline) {
			return Error("conflict", "rollback timer fired before deadline")
		}
		return m.restore(s, id, "network change not confirmed")
	})
	if err != nil {
		return Change{}, err
	}
	return m.Get(id)
}
func (m *Manager) SetService(ctx context.Context, service Service, add bool) error {
	return m.locked(func(s *diskState) error {
		if !add {
			if _, exists := s.Services[service.StackID]; !exists {
				return nil
			}
		}
		if s.Pending != "" {
			return Error("conflict", "network configuration change outstanding")
		}
		if _, err := parseService(service); err != nil {
			return err
		}
		if add {
			live, err := m.Backend.Live(ctx)
			if err != nil {
				return err
			}
			if !Compatible(service.Address, primaryAddresses(live.Addresses, s.Services)) {
				return Error("conflict", "service address is incompatible with LAN subnet")
			}
			if err = validateServices(s.Confirmed, map[string]Service{service.StackID: service}); err != nil {
				return err
			}
			for id, v := range s.Services {
				if id != service.StackID && v.Address == service.Address {
					return Error("conflict", "service address already owned")
				}
			}
			if old, ok := s.Services[service.StackID]; ok && old != service {
				return Error("conflict", "stack already owns another address")
			}
			s.Services[service.StackID] = service
			if err = m.save(s); err != nil {
				return err
			}
			return m.Backend.Service(ctx, service, true)
		}
		existing, ok := s.Services[service.StackID]
		if !ok {
			return nil
		}
		if existing != service {
			return Error("conflict", "service ownership mismatch")
		}
		if err := m.Backend.Service(ctx, existing, false); err != nil {
			return err
		}
		delete(s.Services, service.StackID)
		return m.save(s)
	})
}
func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
func parseService(s Service) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s.Address)
	if err != nil || !p.Addr().Is4() || !p.Addr().IsGlobalUnicast() || p.Bits() < 1 || p.Bits() > 30 || p.Addr() == p.Masked().Addr() || isBroadcast(p.Addr(), p.Bits()) || s.StackID == "" || s.PoolID == "" {
		return netip.Prefix{}, Error("invalid", "invalid service address or ownership")
	}
	return p, nil
}
func Compatible(cidr string, addresses []string) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	for _, value := range addresses {
		host, err := netip.ParsePrefix(value)
		if err == nil && host.Addr().Is4() && host.Masked() == p.Masked() {
			return true
		}
	}
	return false
}

// MigrateDefault only fills an unset default during legacy pool migration.
func (m *Manager) MigrateDefault(poolID string) error {
	return m.locked(func(s *diskState) error {
		if s.Pending != "" {
			return Error("conflict", "network change outstanding")
		}
		if s.Confirmed.DefaultPoolID != "" {
			return nil
		}
		s.Confirmed.DefaultPoolID = poolID
		s.NeedsConfig = true
		if err := m.save(s); err != nil {
			return err
		}
		if err := m.writeConfig(s.Confirmed); err != nil {
			return err
		}
		s.NeedsConfig = false
		return m.save(s)
	})
}

func primaryAddresses(addresses []string, services map[string]Service) []string {
	result := []string{}
	for _, value := range addresses {
		owned := false
		for _, service := range services {
			if service.Address == value {
				owned = true
				break
			}
		}
		if !owned {
			result = append(result, value)
		}
	}
	return result
}

func (m *Manager) SyncServices(ctx context.Context, services []Service) error {
	return m.locked(func(s *diskState) error {
		if s.Pending != "" {
			return Error("conflict", "network change outstanding")
		}
		wanted := map[string]Service{}
		addresses := map[string]bool{}
		live, err := m.Backend.Live(ctx)
		if err != nil {
			return err
		}
		for _, service := range services {
			if _, err = parseService(service); err != nil {
				return err
			}
			if _, exists := wanted[service.StackID]; exists || addresses[service.Address] {
				return Error("invalid", "duplicate service ownership")
			}
			if !Compatible(service.Address, primaryAddresses(live.Addresses, s.Services)) {
				return Error("conflict", "service subnet is not compatible")
			}
			wanted[service.StackID] = service
			addresses[service.Address] = true
		}
		if err = validateServices(s.Confirmed, wanted); err != nil {
			return err
		}
		for id, service := range s.Services {
			if wanted[id] != service {
				if err = m.Backend.Service(ctx, service, false); err != nil {
					return err
				}
				delete(s.Services, id)
				if err = m.save(s); err != nil {
					return err
				}
			}
		}
		for id, service := range wanted {
			if s.Services[id] == service && contains(live.Addresses, service.Address) {
				continue
			}
			s.Services[id] = service
			if err = m.save(s); err != nil {
				return err
			}
			if err = m.Backend.Service(ctx, service, true); err != nil {
				return err
			}
		}
		return nil
	})
}

func (m *Manager) verifyServices(ctx context.Context, services map[string]Service) error {
	if len(services) == 0 {
		return nil
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		live, err := m.Backend.Live(ctx)
		if err != nil {
			return err
		}
		valid := true
		for _, service := range services {
			if !Compatible(service.Address, primaryAddresses(live.Addresses, services)) {
				valid = false
				break
			}
		}
		if valid {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("DHCP lease is incompatible with active service pools: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Retry resolves previously accepted requests without contacting the cluster
// database or waiting for the topology mutation lock.
func (m *Manager) Retry(p ApplyPayload) (*Change, error) {
	if err := p.Config.Validate(); err != nil {
		return nil, err
	}
	s, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	hash := p.Revision + ":" + Revision(p.Config)
	for _, c := range s.Changes {
		if c.Key == p.Key && c.Actor == p.Actor {
			if c.RequestHash != hash {
				return nil, Error("conflict", "idempotency key already used for another request")
			}
			result := c.Change
			return &result, nil
		}
	}
	return nil, nil
}
