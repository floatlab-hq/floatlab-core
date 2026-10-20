package hostupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
)

type Status struct {
	OperationID     string    `json:"operation_id"`
	Actor           string    `json:"actor"`
	State           string    `json:"state"`
	Phase           string    `json:"phase"`
	SourceRelease   string    `json:"source_release"`
	TargetRelease   string    `json:"target_release"`
	Error           string    `json:"error,omitempty"`
	RollbackError   string    `json:"rollback_error,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Manifest        Manifest  `json:"manifest"`
	Source          Manifest  `json:"source_manifest"`
	BootID          string    `json:"boot_id,omitempty"`
	Watchdog        bool      `json:"watchdog"`
	CommitFinalized bool      `json:"commit_finalized,omitempty"`
}

type Backend interface {
	Stage(context.Context, Request) (Manifest, Manifest, error)
	ValidateActivation(context.Context, Status) error
	Arm(context.Context, Status) error
	Clear(context.Context) error
	Reboot(context.Context) error
	Running(context.Context) (version, origin, token, bootID string, err error)
	Health(context.Context) error
	Promote(context.Context, Status) error
	FinalizeCommit(context.Context, Status) error
	RestoreBoot(context.Context, Status) error
	Cleanup(context.Context, Status) error
}

type Manager struct {
	Dir                 string
	Backend             Backend
	Checkpoint          func(context.Context, string) error
	RestoreCheckpoint   func(context.Context, string) error
	ReleaseCheckpoint   func(context.Context, string) error
	PreflightCheckpoint func(context.Context, string) error
	TrialStart          func(context.Context, string) error
	TrialReady          func(string) error
	TrialStop           func(context.Context, string) error
	ValidateManagement  func(context.Context, string) error
	CleanupCheckpoint   func(context.Context, string) error
}

func NewDefault() *Manager { return &Manager{Dir: Directory, Backend: Linux{}} }

func (m *Manager) Status() (Status, error) {
	var s Status
	b, err := os.ReadFile(filepath.Join(m.Dir, "host.json"))
	if os.IsNotExist(err) {
		return Status{State: "idle", Phase: "idle"}, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

var ErrNotFound = errors.New("update operation not found")

func (m *Manager) StatusFor(id string) (Status, error) {
	s, err := m.Status()
	if err != nil {
		return s, err
	}
	if id != "" && s.OperationID != id {
		return s, ErrNotFound
	}
	return s, nil
}

func (m *Manager) save(s *Status, state string) error {
	s.State, s.Phase, s.UpdatedAt = state, state, time.Now().UTC()
	return update.WriteJSON(filepath.Join(m.Dir, "host.json"), s)
}

func terminal(state string) bool {
	return state == "" || state == "idle" || state == "committed" || state == "recovered" || state == "failed"
}

// Prepare returns before downloading; the durable staging checkpoint owns the request.
func (m *Manager) Prepare(ctx context.Context, r Request) (Status, error) {
	if err := r.Validate(); err != nil {
		return Status{}, err
	}
	release, err := update.Acquire(m.Dir)
	if err != nil {
		return Status{}, err
	}
	s, err := m.Status()
	if err != nil {
		release()
		return s, err
	}
	if s.OperationID == r.OperationID {
		release()
		if s.TargetRelease != r.Release {
			return s, fmt.Errorf("operation id already used for another release")
		}
		return s, nil
	}
	if !terminal(s.State) {
		release()
		return s, fmt.Errorf("host update %s is %s", s.OperationID, s.State)
	}
	// Management's durable pending journal also reserves the single update lane.
	var other struct {
		State string `json:"state"`
	}
	b, e := os.ReadFile(filepath.Join(m.Dir, "management.json"))
	if e == nil {
		if e = json.Unmarshal(b, &other); e != nil {
			release()
			return s, e
		}
		if !terminal(other.State) && other.State != "idle" && other.State != "succeeded" && other.State != "rolled_back" {
			release()
			return s, fmt.Errorf("management update is pending")
		}
	} else if !os.IsNotExist(e) {
		release()
		return s, e
	}
	if s.OperationID != "" {
		if err = update.WriteJSON(filepath.Join(m.Dir, "host-history", s.OperationID+".json"), s); err != nil {
			release()
			return s, err
		}
	}
	s = Status{OperationID: r.OperationID, Actor: r.Actor, TargetRelease: r.Release, StartedAt: time.Now().UTC()}
	if state, e := os.ReadFile("/sys/class/watchdog/watchdog0/state"); e == nil && strings.TrimSpace(string(state)) == "active" {
		s.Watchdog = true
	}
	if err = m.save(&s, "staging"); err != nil {
		release()
		return s, err
	}
	accepted := s
	go func() {
		defer release()
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Hour)
		defer cancel()
		target, source, e := m.Backend.Stage(work, r)
		if e == nil && m.PreflightCheckpoint != nil {
			e = m.PreflightCheckpoint(work, r.OperationID)
		}
		s.Manifest, s.Source = target, source
		s.SourceRelease = "host-v" + source.Version
		if e != nil {
			s.Error = e.Error()
			_ = m.save(&s, "failed")
			return
		}
		_ = m.save(&s, "staged")
	}()
	return accepted, nil
}

func (m *Manager) action(ctx context.Context, r Request, action string) (Status, error) {
	release, err := update.Acquire(m.Dir)
	if err != nil {
		return Status{}, err
	}
	defer release()
	return m.actionLocked(ctx, r, action)
}

func (m *Manager) actionLocked(ctx context.Context, r Request, action string) (Status, error) {
	s, err := m.Status()
	if err != nil {
		return s, err
	}
	if !operationID.MatchString(r.OperationID) || r.OperationID != s.OperationID {
		return s, fmt.Errorf("unknown operation")
	}
	switch action {
	case "activate":
		if s.State == "armed" || s.State == "trial" || s.State == "ready" {
			return s, nil
		}
		if s.State != "staged" {
			return s, fmt.Errorf("host update is %s", s.State)
		}
		if m.Checkpoint == nil || m.RestoreCheckpoint == nil || m.ReleaseCheckpoint == nil {
			return s, fmt.Errorf("management recovery coordinator unavailable")
		}
		if err = m.Backend.ValidateActivation(ctx, s); err != nil {
			return s, err
		}
		if m.ValidateManagement == nil {
			return s, fmt.Errorf("management compatibility validator unavailable")
		}
		if err = m.ValidateManagement(ctx, s.Manifest.Version); err != nil {
			return s, err
		}
		_, _, _, s.BootID, err = m.Backend.Running(ctx)
		if err != nil {
			return s, err
		}
		if err = m.save(&s, "arming"); err != nil {
			return s, err
		}
		if err = m.Checkpoint(ctx, s.OperationID); err == nil {
			err = m.Backend.Arm(ctx, s)
		}
		if err != nil {
			return m.restore(ctx, &s, err)
		}
		if err = m.save(&s, "armed"); err != nil {
			return s, err
		}
		if err = m.Backend.Reboot(ctx); err != nil {
			return m.restore(ctx, &s, err)
		}
		return s, nil
	case "commit":
		if s.State == "committed" {
			if !s.CommitFinalized {
				err = m.finishCommit(ctx, &s)
				return s, err
			}
			return s, nil
		}
		if s.State != "ready" && s.State != "committing" {
			return s, fmt.Errorf("trial has not completed stable readiness")
		}
		version, origin, token, _, e := m.Backend.Running(ctx)
		if e != nil {
			return s, e
		}
		if version != s.Manifest.Version || origin != "floatlab/os/"+s.Manifest.Version+"@base" || token != s.OperationID {
			return s, fmt.Errorf("running boot does not match this trial")
		}
		if err = m.Backend.Health(ctx); err != nil {
			return s, err
		}
		if err = m.save(&s, "committing"); err != nil {
			return s, err
		}
		if err = m.Backend.Promote(ctx, s); err != nil {
			return s, err
		}
		// Promotion is durable before policies can allow ordinary service startup.
		if err = m.save(&s, "committed"); err != nil {
			return s, err
		}
		err = m.finishCommit(ctx, &s)
		return s, err
	case "rollback":
		if s.State == "recovered" {
			return s, nil
		}
		if s.State == "staged" || s.State == "failed" {
			if err = m.Backend.Clear(ctx); err != nil {
				return s, err
			}
			err = m.save(&s, "recovered")
			return s, err
		}
		if m.TrialStop == nil {
			return s, fmt.Errorf("management stop coordinator unavailable")
		}
		if err = m.save(&s, "recovering"); err != nil {
			return s, err
		}
		if err = m.Backend.Clear(ctx); err != nil {
			return s, err
		}
		if err = m.TrialStop(ctx, s.OperationID); err != nil {
			return s, err
		}
		if err = m.Backend.RestoreBoot(ctx, s); err != nil {
			return s, err
		}
		return s, m.Backend.Reboot(ctx)
	case "cleanup":
		if !terminal(s.State) {
			return s, fmt.Errorf("cleanup blocked by pending trial")
		}
		return s, m.cleanup(ctx, s)
	}
	return s, fmt.Errorf("unknown host update action")
}

func (m *Manager) Activate(ctx context.Context, r Request) (Status, error) {
	return m.action(ctx, r, "activate")
}
func (m *Manager) Commit(ctx context.Context, r Request) (Status, error) {
	return m.action(ctx, r, "commit")
}
func (m *Manager) Rollback(ctx context.Context, r Request) (Status, error) {
	return m.action(ctx, r, "rollback")
}
func (m *Manager) Cleanup(ctx context.Context, r Request) (Status, error) {
	return m.action(ctx, r, "cleanup")
}

// Submit durably accepts an action before returning to the management API.
func (m *Manager) Submit(ctx context.Context, r Request, action string) (Status, error) {
	if !operationID.MatchString(r.OperationID) || r.IdempotencyKey == "" || len(r.IdempotencyKey) > 255 || r.Actor == "" {
		return Status{}, fmt.Errorf("operation_id, idempotency_key and actor required")
	}
	release, err := update.Acquire(m.Dir)
	if err != nil {
		return Status{}, err
	}
	s, err := m.Status()
	if err != nil {
		release()
		return s, err
	}
	if s.OperationID != r.OperationID {
		release()
		return s, fmt.Errorf("unknown operation")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(r.IdempotencyKey)))
	path := filepath.Join(m.Dir, "host-actions", key+".json")
	var previous struct {
		Request Request `json:"request"`
		Action  string  `json:"action"`
	}
	b, e := os.ReadFile(path)
	if e == nil {
		release()
		if e = json.Unmarshal(b, &previous); e != nil {
			return s, e
		}
		if previous.Request.OperationID != r.OperationID || previous.Action != action {
			return s, fmt.Errorf("idempotency key already used")
		}
		return s, nil
	}
	if !os.IsNotExist(e) {
		release()
		return s, e
	}
	switch action {
	case "activate":
		if s.State != "staged" {
			release()
			return s, fmt.Errorf("host update is %s", s.State)
		}
	case "commit":
		if s.State != "ready" && s.State != "committing" {
			release()
			return s, fmt.Errorf("trial is not ready")
		}
	case "cleanup":
		if !terminal(s.State) {
			release()
			return s, fmt.Errorf("update is pending")
		}
	case "rollback":
	default:
		release()
		return s, fmt.Errorf("invalid action")
	}
	if err = update.WriteJSON(path, map[string]any{"request": r, "action": action}); err != nil {
		release()
		return s, err
	}
	s.Phase = action + "_requested"
	s.UpdatedAt = time.Now().UTC()
	if err = update.WriteJSON(filepath.Join(m.Dir, "host.json"), s); err != nil {
		release()
		return s, err
	}
	go func() {
		defer release()
		work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Minute)
		defer cancel()
		result, e := m.actionLocked(work, r, action)
		if e != nil {
			result.Error = e.Error()
		}
		if strings.HasSuffix(result.Phase, "_requested") {
			result.Phase = result.State
		}
		result.UpdatedAt = time.Now().UTC()
		_ = update.WriteJSON(filepath.Join(m.Dir, "host.json"), result)
	}()
	return s, nil
}

func (m *Manager) restore(ctx context.Context, s *Status, cause error) (Status, error) {
	if cause != nil {
		s.Error = cause.Error()
	}
	if err := m.save(s, "recovering"); err != nil {
		return *s, err
	}
	if err := m.Backend.Clear(ctx); err != nil {
		s.RollbackError = err.Error()
		_ = m.save(s, "recovering")
		return *s, err
	}
	if err := m.Backend.RestoreBoot(ctx, *s); err != nil {
		s.RollbackError = err.Error()
		_ = m.save(s, "recovering")
		return *s, err
	}
	if m.RestoreCheckpoint == nil {
		return *s, fmt.Errorf("management restore coordinator unavailable")
	}
	if err := m.RestoreCheckpoint(ctx, s.OperationID); err != nil {
		s.RollbackError = err.Error()
		_ = m.save(s, "recovering")
		return *s, err
	}
	s.RollbackError = ""
	err := m.save(s, "recovered")
	return *s, err
}

// Recover is called before every core-stack startup, including fallback boots.
func (m *Manager) Recover(ctx context.Context) error {
	release, err := update.Acquire(m.Dir)
	if err != nil {
		return err
	}
	defer release()
	s, err := m.Status()
	if err != nil {
		return err
	}
	if strings.HasSuffix(s.Phase, "_requested") {
		_, err = m.actionLocked(ctx, Request{OperationID: s.OperationID, Actor: s.Actor}, strings.TrimSuffix(s.Phase, "_requested"))
		return err
	}
	if terminal(s.State) {
		if s.State == "committed" && !s.CommitFinalized {
			return m.finishCommit(ctx, &s)
		}
		return nil
	}
	if s.State == "staging" {
		s.Error = "OS staging interrupted; retry with a new operation"
		return m.save(&s, "failed")
	}
	if s.State == "staged" {
		return nil
	}
	version, origin, token, boot, err := m.Backend.Running(ctx)
	if err != nil {
		return err
	}
	if s.State == "recovering" && version == s.Manifest.Version && origin == "floatlab/os/"+s.Manifest.Version+"@base" {
		if m.TrialStop == nil {
			return fmt.Errorf("management stop coordinator unavailable")
		}
		if err = m.TrialStop(ctx, s.OperationID); err != nil {
			return err
		}
		if err = m.Backend.Clear(ctx); err != nil {
			return err
		}
		if err = m.Backend.RestoreBoot(ctx, s); err != nil {
			return err
		}
		return m.Backend.Reboot(ctx)
	}
	if token == s.OperationID && version == s.Manifest.Version && origin == "floatlab/os/"+s.Manifest.Version+"@base" && s.State != "recovering" {
		if s.State == "committing" {
			if err = m.Backend.Promote(ctx, s); err != nil {
				return err
			}
			if err = m.save(&s, "committed"); err != nil {
				return err
			}
			return m.finishCommit(ctx, &s)
		}
		if m.TrialStart == nil {
			return fmt.Errorf("management trial coordinator unavailable")
		}
		if err = m.TrialStart(ctx, s.OperationID); err != nil {
			return err
		}
		if s.State == "ready" {
			return nil
		}
		return m.save(&s, "trial")
	}
	if boot == s.BootID && s.State == "armed" {
		return fmt.Errorf("OS reboot is pending")
	}
	if version != s.Source.Version {
		return fmt.Errorf("recovery boot is neither source nor authorized trial; management remains stopped")
	}
	_, err = m.restore(ctx, &s, errors.New("uncommitted OS trial returned to committed boot"))
	return err
}

// Supervise keeps the independent guard informed only while real readiness holds.
func (m *Manager) Supervise(ctx context.Context) error {
	s, err := m.Status()
	if err != nil || s.State != "trial" && s.State != "ready" {
		return err
	}
	stable := time.Time{}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		current, e := m.Status()
		if e != nil {
			return e
		}
		if current.State == "committed" {
			if current.CommitFinalized {
				return WriteGuardMessage(s.OperationID, "committed")
			}
			continue
		}
		if current.State == "recovering" {
			return nil
		}
		if err = m.Backend.Health(ctx); err != nil {
			stable = time.Time{}
			continue
		}
		now := time.Now()
		if stable.IsZero() {
			stable = now
		}
		if current.State != "ready" && now.Sub(stable) < time.Minute {
			continue
		}
		if current.State != "ready" {
			release, e := update.Acquire(m.Dir)
			if e != nil {
				continue
			}
			fresh, e := m.Status()
			if e == nil && (fresh.State == "trial" || fresh.State == "ready") {
				e = m.save(&fresh, "ready")
			}
			release()
			if e != nil {
				return e
			}
		}
		if m.TrialReady != nil {
			if e = m.TrialReady(s.OperationID); e != nil {
				return e
			}
		}
		if err = WriteGuardMessage(s.OperationID, "ready"); err != nil {
			return err
		}
	}
}

func (m *Manager) finishCommit(ctx context.Context, s *Status) error {
	if m.ReleaseCheckpoint == nil {
		return fmt.Errorf("management coordinator unavailable")
	}
	if err := m.ReleaseCheckpoint(ctx, s.OperationID); err != nil {
		return err
	}
	if err := m.Backend.FinalizeCommit(ctx, *s); err != nil {
		return err
	}
	s.CommitFinalized = true
	if err := m.save(s, "committed"); err != nil {
		return err
	}
	if err := m.cleanup(ctx, *s); err != nil {
		s.Error = "committed; cleanup pending: " + err.Error()
		_ = m.save(s, "committed")
	}
	return nil
}
func (m *Manager) cleanup(ctx context.Context, s Status) error {
	if err := m.Backend.Cleanup(ctx, s); err != nil {
		return err
	}
	if s.State != "committed" {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(m.Dir, "host-history"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !operationID.MatchString(id) {
			return fmt.Errorf("invalid historical operation")
		}
		if id == s.OperationID {
			continue
		}
		if m.CleanupCheckpoint == nil {
			return fmt.Errorf("management checkpoint cleanup unavailable")
		}
		if err = m.CleanupCheckpoint(ctx, id); err != nil {
			return err
		}
		if err = os.Remove(filepath.Join(m.Dir, "host-history", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
