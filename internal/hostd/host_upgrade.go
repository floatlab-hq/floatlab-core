package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"syscall"
	"time"

	"github.com/floatlab/floatlab-core/internal/hostupgrade"
	"github.com/floatlab/floatlab-core/internal/managementupdate"
	"github.com/floatlab/floatlab-core/pkg/ipc"
)

// Core startup may be reconciling the same journal; supervision must follow its completion.
func waitForUpdateRecovery(ctx context.Context, recover func(context.Context) error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := recover(ctx)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func NewHostUpdateCoordinator(management *managementupdate.Manager) *hostupgrade.Manager {
	m := hostupgrade.NewDefault()
	m.Checkpoint = management.Checkpoint
	m.RestoreCheckpoint = management.RestoreCheckpoint
	m.ReleaseCheckpoint = management.ReleaseCheckpoint
	m.PreflightCheckpoint = management.PreflightCheckpoint
	m.TrialStart = management.StartCheckpointTrial
	m.TrialReady = management.ActivateCheckpointControllers
	m.TrialStop = management.StopCheckpoint
	m.ValidateManagement = management.ValidateCheckpointHost
	m.CleanupCheckpoint = management.CleanupCheckpoint
	return m
}

func registerHostUpdates(server *ipc.Server, m *hostupgrade.Manager) {
	server.Handle("host.update.releases", func(ctx context.Context, _ json.RawMessage) (any, error) { return hostupgrade.Releases(ctx) })
	server.Handle("host.update.status", func(_ context.Context, b json.RawMessage) (any, error) {
		var r hostupgrade.Request
		if len(b) > 0 {
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
			}
		}
		s, err := m.StatusFor(r.OperationID)
		if errors.Is(err, hostupgrade.ErrNotFound) {
			return nil, &ipc.RPCError{Code: "update.not_found", Message: err.Error()}
		}
		return s, err
	})
	server.Handle("host.update.prepare", func(ctx context.Context, b json.RawMessage) (any, error) {
		var r hostupgrade.Request
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
		}
		if err := r.Validate(); err != nil {
			return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
		}
		s, err := m.Prepare(ctx, r)
		if err != nil {
			return nil, &ipc.RPCError{Code: "update.conflict", Message: err.Error()}
		}
		return s, nil
	})
	for _, name := range []string{"activate", "commit", "rollback", "cleanup"} {
		server.Handle("host.update."+name, func(ctx context.Context, b json.RawMessage) (any, error) {
			var r hostupgrade.Request
			if err := json.Unmarshal(b, &r); err != nil {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
			}
			if r.OperationID == "" || r.Actor == "" || r.IdempotencyKey == "" {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: "operation_id, actor and idempotency_key required"}
			}
			s, err := m.Submit(ctx, r, name)
			if err != nil {
				return nil, &ipc.RPCError{Code: "update.conflict", Message: err.Error()}
			}
			return s, nil
		})
	}
}
