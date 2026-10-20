// Package managementupdate owns the offline management upgrade transaction.
package managementupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/release"
)

const Directory = "/floatlab/system/updates"

type Request struct {
	OperationID    string `json:"operation_id"`
	Release        string `json:"release"`
	Actor          string `json:"actor"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}
type Status struct {
	OperationID       string            `json:"operation_id"`
	Actor             string            `json:"actor"`
	Phase             string            `json:"phase"`
	State             string            `json:"state"`
	SourceRelease     string            `json:"source_release"`
	TargetRelease     string            `json:"target_release"`
	Error             string            `json:"error,omitempty"`
	RollbackError     string            `json:"rollback_error,omitempty"`
	StartedAt         time.Time         `json:"started_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
	Manifest          Manifest          `json:"manifest"`
	PreviousCompose   []byte            `json:"previous_compose,omitempty"`
	CandidateCompose  []byte            `json:"candidate_compose,omitempty"`
	Datasets          []Dataset         `json:"datasets,omitempty"`
	Policies          map[string]string `json:"policies,omitempty"`
	Snapshot          string            `json:"snapshot,omitempty"`
	Authorized        bool              `json:"authorized"`
	ActionKeys        map[string]string `json:"action_keys,omitempty"`
	ProductionChanged bool              `json:"production_changed"`
	CleanupState      string            `json:"cleanup_state,omitempty"`
}
type Dataset struct {
	Name string `json:"name"`
	GUID string `json:"guid"`
}
type Backend interface {
	Prepare(context.Context, *Status) error
	Preflight(context.Context, *Status) error
	Fence(context.Context, *Status) error
	Snapshot(context.Context, *Status) error
	Install(context.Context, *Status) error
	Healthy(context.Context, *Status) error
	Restore(context.Context, *Status) error
	Authorize(context.Context, *Status) error
	Cleanup(context.Context, *Status) error
}

type Manager struct {
	Dir     string
	Backend Backend
	mu      sync.Mutex
	running bool
}

var operationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
var ErrNotFound = errors.New("operation not found")
var ErrInvalid = errors.New("invalid update request")

func NewDefault() *Manager      { return &Manager{Dir: Directory, Backend: &Linux{Dir: Directory}} }
func (m *Manager) path() string { return filepath.Join(m.Dir, "management.json") }
func (m *Manager) save(s *Status) error {
	s.UpdatedAt = time.Now().UTC()
	return update.WriteJSON(m.path(), s)
}
func (m *Manager) Status(id string) (*Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read(id)
}
func (m *Manager) read(id string) (*Status, error) {
	var s Status
	b, e := os.ReadFile(m.path())
	if os.IsNotExist(e) {
		if id != "" {
			return nil, ErrNotFound
		}
		return &Status{State: "idle", Phase: "idle"}, nil
	}
	if e != nil {
		return nil, e
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return nil, e
	}
	if id != "" && s.OperationID != id {
		return nil, ErrNotFound
	}
	return &s, nil
}
func terminal(s *Status) bool {
	return s.State == "idle" || s.State == "succeeded" || s.State == "failed" || s.State == "rolled_back"
}
func (m *Manager) Start(ctx context.Context, r Request) (*Status, error) {
	if !operationID.MatchString(r.OperationID) || r.Actor == "" || len(r.Actor) > 256 || !validRelease(r.Release) {
		return nil, fmt.Errorf("%w: valid operation_id, actor and official manage-v release required", ErrInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, e := m.read("")
	if e != nil {
		return nil, e
	}
	if old.OperationID == r.OperationID {
		if old.TargetRelease != r.Release {
			return nil, fmt.Errorf("operation ID already used for another release")
		}
		return old, nil
	}
	if b, err := os.ReadFile(filepath.Join(m.Dir, "management-history", r.OperationID+".json")); err == nil {
		var previous Status
		if err = json.Unmarshal(b, &previous); err != nil {
			return nil, err
		}
		if previous.TargetRelease != r.Release {
			return nil, fmt.Errorf("operation ID already used for another release")
		}
		return &previous, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if m.running || !terminal(old) {
		return nil, fmt.Errorf("management recovery is pending")
	}
	unlock, e := update.Acquire(m.Dir)
	if e != nil {
		return nil, e
	}
	if err := m.hostAvailable(); err != nil {
		unlock()
		return nil, err
	}
	if old.OperationID != "" {
		if err := update.WriteJSON(filepath.Join(m.Dir, "management-history", old.OperationID+".json"), old); err != nil {
			unlock()
			return nil, err
		}
	}
	s := &Status{OperationID: r.OperationID, Actor: r.Actor, TargetRelease: r.Release, State: "running", Phase: "prepare", StartedAt: time.Now().UTC()}
	if e = m.save(s); e != nil {
		unlock()
		return nil, e
	}
	m.running = true
	copy := *s
	go func() {
		defer unlock()
		defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
		m.execute(ctx, s)
	}()
	return &copy, nil
}
func (m *Manager) step(ctx context.Context, s *Status, phase string, fn func(context.Context, *Status) error) error {
	m.mu.Lock()
	s.Phase = phase
	err := m.save(s)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	err = fn(ctx, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if saveErr := m.save(s); err == nil {
		err = saveErr
	}
	return err
}
func (m *Manager) execute(ctx context.Context, s *Status) {
	for _, step := range []struct {
		name string
		fn   func(context.Context, *Status) error
	}{{"prepare", m.Backend.Prepare}, {"preflight", m.Backend.Preflight}, {"fencing", m.Backend.Fence}, {"snapshot", m.Backend.Snapshot}, {"installing", m.Backend.Install}, {"checking", m.Backend.Healthy}} {
		if step.name == "snapshot" {
			s.Snapshot = "floatlab-update-" + s.OperationID
		}
		if step.name == "installing" {
			s.ProductionChanged = true
		}
		if err := m.step(ctx, s, step.name, step.fn); err != nil {
			s.Error = err.Error()
			if s.Phase == "prepare" || s.Phase == "preflight" {
				s.State = "failed"
				_ = m.Backend.Cleanup(context.WithoutCancel(ctx), s)
				m.mu.Lock()
				_ = m.save(s)
				m.mu.Unlock()
				return
			}
			_ = m.rollback(context.WithoutCancel(ctx), s)
			return
		}
		if step.name == "preflight" {
			if err := m.Backend.Cleanup(ctx, s); err != nil {
				s.Error = "preflight resource cleanup: " + err.Error()
				s.State = "failed"
				m.mu.Lock()
				_ = m.save(s)
				m.mu.Unlock()
				return
			}
		}
	}
	s.Authorized = true
	if err := m.step(ctx, s, "authorizing", m.Backend.Authorize); err != nil {
		s.Error = err.Error()
		_ = m.rollback(context.WithoutCancel(ctx), s)
		return
	}
	s.State = "succeeded"
	s.Phase = "committed"
	m.mu.Lock()
	_ = m.save(s)
	m.mu.Unlock()
	if err := m.PruneHistory(context.WithoutCancel(ctx)); err != nil {
		s.Error = "committed; previous resource cleanup pending: " + err.Error()
		m.mu.Lock()
		_ = m.save(s)
		m.mu.Unlock()
	}
}
func (m *Manager) rollback(ctx context.Context, s *Status) error {
	s.State = "recovering"
	s.Authorized = false
	if err := m.step(ctx, s, "restoring", m.Backend.Restore); err != nil {
		s.State = "recovery_failed"
		s.RollbackError = err.Error()
		m.mu.Lock()
		_ = m.save(s)
		m.mu.Unlock()
		return err
	}
	if err := m.step(ctx, s, "verifying_previous", m.Backend.Healthy); err != nil {
		s.State = "recovery_failed"
		s.RollbackError = err.Error()
		m.mu.Lock()
		_ = m.save(s)
		m.mu.Unlock()
		return err
	}
	s.Authorized = true
	if err := m.step(ctx, s, "authorizing_previous", m.Backend.Authorize); err != nil {
		return err
	}
	s.State = "rolled_back"
	s.Phase = "rolled_back"
	s.RollbackError = ""
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.save(s)
}

// Recover runs before any core-stack startup; uncertain production transitions restore the old checkpoint.
func (m *Manager) Recover(ctx context.Context) error {
	unlock, e := update.Acquire(m.Dir)
	if e != nil {
		return e
	}
	defer unlock()
	s, e := m.Status("")
	if e != nil {
		return e
	}
	if terminal(s) {
		return nil
	}
	if s.Phase == "cleaning" {
		disposable := *s
		disposable.Snapshot = ""
		if e = m.Backend.Cleanup(ctx, &disposable); e != nil {
			return e
		}
		s.State = s.CleanupState
		if s.State == "" {
			s.State = "succeeded"
		}
		s.Phase = "cleaned"
		return m.save(s)
	}
	if s.Authorized {
		if e = m.Backend.Authorize(ctx, s); e != nil {
			return e
		}
		if s.State == "recovering" {
			s.State = "rolled_back"
			s.Phase = "rolled_back"
			s.RollbackError = ""
		} else {
			s.State = "succeeded"
			s.Phase = "committed"
		}
		return m.save(s)
	}
	if s.Phase == "prepare" || s.Phase == "preflight" {
		s.State = "failed"
		s.Error = "interrupted before production changes"
		if e = m.Backend.Cleanup(ctx, s); e != nil {
			return e
		}
		return m.save(s)
	}
	return m.rollback(ctx, s)
}

// PruneHistory only releases an older management rollback point after a newer transaction committed.
func (m *Manager) PruneHistory(ctx context.Context) error {
	current, err := m.Status("")
	if err != nil {
		return err
	}
	if current.State != "succeeded" || current.Snapshot == "" {
		return nil
	}
	entries, e := os.ReadDir(filepath.Join(m.Dir, "management-history"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(m.Dir, "management-history", entry.Name())
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		var s Status
		if e = json.Unmarshal(b, &s); e != nil {
			return e
		}
		if !operationID.MatchString(s.OperationID) || entry.Name() != s.OperationID+".json" {
			return fmt.Errorf("invalid historical operation identity")
		}
		if e = m.Backend.Cleanup(ctx, &s); e != nil {
			return e
		}
		if linux, ok := m.Backend.(*Linux); ok {
			if e = linux.releaseImages(ctx, &s); e != nil {
				return e
			}
		}
		s.Snapshot = ""
		s.PreviousCompose = nil
		s.CandidateCompose = nil
		s.Manifest.Images = nil
		if e = update.WriteJSON(path, &s); e != nil {
			return e
		}
	}
	return nil
}
func (m *Manager) Rollback(ctx context.Context, id string) (*Status, error) {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return nil, fmt.Errorf("transaction active")
	}
	m.mu.Unlock()
	unlock, e := update.Acquire(m.Dir)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s, e := m.Status(id)
	if e != nil {
		return nil, e
	}
	if s.Snapshot == "" {
		return nil, fmt.Errorf("no retained rollback checkpoint")
	}
	e = m.rollback(ctx, s)
	return s, e
}
func (m *Manager) Cleanup(ctx context.Context, id string) (*Status, error) {
	unlock, e := update.Acquire(m.Dir)
	if e != nil {
		return nil, e
	}
	defer unlock()
	s, e := m.Status(id)
	if e != nil {
		return nil, e
	}
	if !terminal(s) {
		return nil, fmt.Errorf("cannot clean up a pending transaction")
	}
	disposable := *s
	disposable.Snapshot = ""
	if e = m.Backend.Cleanup(ctx, &disposable); e != nil {
		return nil, e
	}
	if e = m.PruneHistory(ctx); e != nil {
		return nil, e
	}
	return s, m.save(s)
}

// Checkpoint is called while the host updater owns the shared update lock.
func (m *Manager) Checkpoint(ctx context.Context, id string) error {
	if !operationID.MatchString(id) {
		return fmt.Errorf("invalid operation ID")
	}
	s := &Status{OperationID: id, State: "os_checkpoint", Phase: "fencing"}
	l, ok := m.Backend.(*Linux)
	if !ok {
		return errors.New("OS checkpoint requires system backend")
	}
	if err := l.capture(ctx, s); err != nil {
		return err
	}
	path := filepath.Join(m.Dir, "management-os-"+id+".json")
	if err := update.WriteJSON(path, s); err != nil {
		return err
	}
	if err := l.Fence(ctx, s); err != nil {
		return err
	}
	s.Snapshot = "floatlab-update-" + id
	if err := update.WriteJSON(path, s); err != nil {
		return err
	}
	if err := l.Snapshot(ctx, s); err != nil {
		return err
	}
	s.ProductionChanged = true
	s.Phase = "checkpoint_ready"
	return update.WriteJSON(path, s)
}
func (m *Manager) checkpoint(id string) (*Status, string, error) {
	if !operationID.MatchString(id) {
		return nil, "", fmt.Errorf("invalid operation ID")
	}
	path := filepath.Join(m.Dir, "management-os-"+id+".json")
	b, e := os.ReadFile(path)
	var s Status
	if e == nil {
		e = json.Unmarshal(b, &s)
	}
	return &s, path, e
}
func (m *Manager) RestoreCheckpoint(ctx context.Context, id string) error {
	s, path, e := m.checkpoint(id)
	if e != nil {
		return e
	}
	if e = m.Backend.Restore(ctx, s); e != nil {
		return e
	}
	if e = m.Backend.Healthy(ctx, s); e != nil {
		return e
	}
	s.Authorized = true
	if e = update.WriteJSON(path, s); e != nil {
		return e
	}
	return m.Backend.Authorize(ctx, s)
}
func (m *Manager) ReleaseCheckpoint(ctx context.Context, id string) error {
	s, path, e := m.checkpoint(id)
	if e != nil {
		return e
	}
	s.Authorized = true
	if e = update.WriteJSON(path, s); e != nil {
		return e
	}
	return m.Backend.Authorize(ctx, s)
}

func (m *Manager) RequestRollback(ctx context.Context, r Request) (*Status, error) {
	return m.requestAction(ctx, r, "rollback")
}
func (m *Manager) RequestCleanup(ctx context.Context, r Request) (*Status, error) {
	return m.requestAction(ctx, r, "cleanup")
}
func (m *Manager) requestAction(ctx context.Context, r Request, action string) (*Status, error) {
	if !operationID.MatchString(r.OperationID) || (len(r.IdempotencyKey) == 0 || len(r.IdempotencyKey) > 255) || r.Actor == "" {
		return nil, fmt.Errorf("valid operation_id, idempotency_key and actor required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, e := m.read(r.OperationID)
	if e != nil {
		return nil, e
	}
	if s.ActionKeys[action] == r.IdempotencyKey {
		return s, nil
	}
	if m.running || !terminal(s) {
		return nil, fmt.Errorf("transaction active or recovery pending")
	}
	if e = m.hostAvailable(); e != nil {
		return nil, e
	}
	unlock, e := update.Acquire(m.Dir)
	if e != nil {
		return nil, e
	}
	if action == "rollback" && s.Snapshot == "" {
		unlock()
		return nil, fmt.Errorf("no retained rollback checkpoint")
	}
	if s.ActionKeys == nil {
		s.ActionKeys = map[string]string{}
	}
	s.ActionKeys[action] = r.IdempotencyKey
	if action == "cleanup" {
		s.CleanupState = s.State
	}
	s.State = "running"
	s.Phase = "restoring"
	if action == "rollback" {
		s.Authorized = false
	}
	if action == "cleanup" {
		s.Phase = "cleaning"
	}
	if e = m.save(s); e != nil {
		unlock()
		return nil, e
	}
	copy := *s
	m.running = true
	go func() {
		defer unlock()
		defer func() { m.mu.Lock(); m.running = false; m.mu.Unlock() }()
		if action == "rollback" {
			_ = m.rollback(ctx, s)
		} else {
			disposable := *s
			disposable.Snapshot = ""
			if e := m.Backend.Cleanup(ctx, &disposable); e != nil {
				s.Error = e.Error()
				s.State = "failed"
			} else {
				s.State = s.CleanupState
				if s.State == "" {
					s.State = "succeeded"
				}
				s.Phase = "cleaned"
			}
			m.mu.Lock()
			_ = m.save(s)
			m.mu.Unlock()
			if s.State == "succeeded" {
				_ = m.PruneHistory(ctx)
			}
		}
	}()
	return &copy, nil
}

func (m *Manager) StartCheckpointTrial(ctx context.Context, id string) error {
	s, _, e := m.checkpoint(id)
	if e != nil {
		return e
	}
	if e = update.WriteFile(filepath.Join(activationDir, "blocked"), []byte(id), 0600); e != nil {
		return e
	}
	l, ok := m.Backend.(*Linux)
	if !ok {
		return fmt.Errorf("system backend required")
	}
	if e = l.verifyMountedDatasets(ctx, s.Datasets); e != nil {
		return e
	}
	b, e := l.noRestart(s.PreviousCompose)
	if e != nil {
		return e
	}
	if e = update.WriteFile(composePath, b, 0644); e != nil {
		return e
	}
	return l.compose(ctx, composePath, "floatlab-system", "up", "-d", "--pull", "never", "--wait", "--wait-timeout", "300")
}
func (m *Manager) CheckpointHealthy(ctx context.Context, id string) error {
	s, _, e := m.checkpoint(id)
	if e != nil {
		return e
	}
	return m.Backend.Healthy(ctx, s)
}
func (m *Manager) ActivateCheckpointControllers(id string) error {
	if _, _, e := m.checkpoint(id); e != nil {
		return e
	}
	e := os.Remove(filepath.Join(activationDir, "blocked"))
	if os.IsNotExist(e) {
		return nil
	}
	return e
}

// CleanupCheckpoint is called under the host coordinator's shared lock after a later host commitment.
func (m *Manager) CleanupCheckpoint(ctx context.Context, id string) error {
	s, path, e := m.checkpoint(id)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if e = m.Backend.Cleanup(ctx, s); e != nil {
		return e
	}
	if e = os.Remove(path); e != nil {
		return e
	}
	dir, e := os.Open(m.Dir)
	if e != nil {
		return e
	}
	defer dir.Close()
	return dir.Sync()
}

func (m *Manager) StopCheckpoint(ctx context.Context, id string) error {
	s, _, e := m.checkpoint(id)
	if e != nil {
		return e
	}
	return m.Backend.Fence(ctx, s)
}
func (m *Manager) ValidateCheckpointHost(ctx context.Context, version string) error {
	l, ok := m.Backend.(*Linux)
	if !ok {
		return fmt.Errorf("system backend required")
	}
	b, e := l.run(ctx, "docker", "exec", l.controlID(ctx), "/usr/local/bin/floatlab-control", "--release-info")
	if e != nil {
		return e
	}
	var info struct {
		RequiredHost release.HostRange `json:"required_host"`
	}
	if e = json.Unmarshal([]byte(b), &info); e != nil {
		return e
	}
	ok, e = info.RequiredHost.Contains(version)
	if e != nil {
		return e
	}
	if !ok {
		return fmt.Errorf("installed management is incompatible with target host %s", version)
	}
	return nil
}
func (m *Manager) PreflightCheckpoint(ctx context.Context, id string) error {
	if !operationID.MatchString(id) {
		return fmt.Errorf("invalid operation ID")
	}
	l, ok := m.Backend.(*Linux)
	if !ok {
		return fmt.Errorf("system backend required")
	}
	s := &Status{OperationID: id}
	if e := l.capture(ctx, s); e != nil {
		return e
	}
	doc, e := composeDocument(s.PreviousCompose)
	if e != nil {
		return e
	}
	s.Manifest.Images = map[string]string{}
	for name, service := range services(doc) {
		s.Manifest.Images[name] = service["image"].(string)
	}
	e = l.Preflight(ctx, s)
	cleanupErr := l.Cleanup(context.WithoutCancel(ctx), s)
	return errors.Join(e, cleanupErr)
}

func (m *Manager) hostAvailable() error {
	b, e := os.ReadFile(filepath.Join(m.Dir, "host.json"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	var s struct {
		State string `json:"state"`
	}
	if e = json.Unmarshal(b, &s); e != nil {
		return e
	}
	switch s.State {
	case "", "committed", "recovered", "failed":
		return nil
	}
	return fmt.Errorf("host update %s blocks management updates", s.State)
}
