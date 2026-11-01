package hostd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/floatlab/floatlab-core/internal/managementupdate"
	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/release"
)

// registerPreflightReads is deliberately independent of the production registry.
// New production commands never become available to an untrusted candidate implicitly.
func (d *Dispatcher) registerPreflightReads(s *ipc.Server) {
	s.Handle("sys.info", d.sysInfo)
	s.Handle("sys.version", func(context.Context, json.RawMessage) (any, error) {
		return ipc.VersionResult{HostVersion: release.Version, GitCommit: release.GitCommit}, nil
	})
	s.Handle("docker.list", d.dockerList)
	s.Handle("fs.pool.list", d.poolList)
	s.Handle("fs.pool.health", d.poolHealth)
	s.Handle("fs.dataset.list", d.datasetList)
	s.Handle("fs.snapshot.list", d.snapshotList)
}
func publicManagementStatus(s *managementupdate.Status) any {
	if s == nil {
		return nil
	}
	return struct {
		Upstream      string `json:"upstream,omitempty"`
		SkipDryRun    bool   `json:"skip_dry_run,omitempty"`
		OperationID   string `json:"operation_id"`
		Actor         string `json:"actor"`
		State         string `json:"state"`
		Phase         string `json:"phase"`
		SourceRelease string `json:"source_release"`
		TargetRelease string `json:"target_release"`
		Error         string `json:"error,omitempty"`
		RollbackError string `json:"rollback_error,omitempty"`
		StartedAt     any    `json:"started_at"`
		UpdatedAt     any    `json:"updated_at"`
	}{s.Upstream, s.SkipDryRun, s.OperationID, s.Actor, s.State, s.Phase, s.SourceRelease, s.TargetRelease, s.Error, s.RollbackError, s.StartedAt, s.UpdatedAt}
}
func registerManagementUpdates(ctx context.Context, s *ipc.Server, m *managementupdate.Manager) {
	s.Handle("management.update.releases", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var r managementupdate.Request
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
			}
		}
		if err := update.ValidateUpstream(r.Upstream); err != nil {
			return nil, &ipc.RPCError{Code: "update.invalid", Message: err.Error()}
		}
		return managementupdate.Releases(ctx, r.Upstream)
	})
	s.Handle("management.update.status", func(_ context.Context, raw json.RawMessage) (any, error) {
		var r managementupdate.Request
		if e := json.Unmarshal(raw, &r); e != nil {
			return nil, e
		}
		status, e := m.Status(r.OperationID)
		if errors.Is(e, managementupdate.ErrNotFound) {
			return nil, &ipc.RPCError{Code: "update.not_found", Message: e.Error()}
		}
		return publicManagementStatus(status), e
	})
	s.Handle("management.update.start", func(_ context.Context, raw json.RawMessage) (any, error) {
		var r managementupdate.Request
		if e := json.Unmarshal(raw, &r); e != nil {
			return nil, &ipc.RPCError{Code: "update.invalid", Message: e.Error()}
		}
		status, e := m.Start(ctx, r)
		if e != nil {
			if errors.Is(e, managementupdate.ErrInvalid) {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: e.Error()}
			}
			return nil, &ipc.RPCError{Code: "update.conflict", Message: e.Error()}
		}
		return publicManagementStatus(status), nil
	})
	for _, action := range []string{"rollback", "cleanup"} {
		action := action
		s.Handle("management.update."+action, func(_ context.Context, raw json.RawMessage) (any, error) {
			var r managementupdate.Request
			if e := json.Unmarshal(raw, &r); e != nil {
				return nil, e
			}
			if r.OperationID == "" || r.Actor == "" || r.IdempotencyKey == "" {
				return nil, &ipc.RPCError{Code: "update.invalid", Message: "operation_id, actor and idempotency_key required"}
			}
			var status *managementupdate.Status
			var e error
			if action == "rollback" {
				status, e = m.RequestRollback(ctx, r)
			} else {
				status, e = m.RequestCleanup(ctx, r)
			}
			if e != nil {
				return nil, &ipc.RPCError{Code: "update.conflict", Message: e.Error()}
			}
			return publicManagementStatus(status), nil
		})
	}
}

func managementStartupBlocked() error {
	_, e := os.Stat(filepath.Join(managementupdate.Directory, "management-blocked"))
	if e == nil {
		return fmt.Errorf("management update reconciliation has not authorized startup")
	}
	if !os.IsNotExist(e) {
		return e
	}
	return nil
}
