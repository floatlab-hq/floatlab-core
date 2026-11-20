package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestBackupStopPreservesHostAcrossFailureAndRestart(t *testing.T) {
	for _, test := range []struct {
		name            string
		restart, legacy bool
	}{{name: "live"}, {name: "resumed", restart: true}, {name: "legacy", restart: true, legacy: true}} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "backup")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := rqlite.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			if err := config.Migrate(ctx, db); err != nil {
				t.Fatal(err)
			}
			cfg := config.NewStore(db)
			if err := cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "primary", BackupNodeID: "backup", ZFSDataset: "floatlab/web"}); err != nil {
				t.Fatal(err)
			}
			node, err := floatraft.NewLocalNode(ctx, db, "backup", zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			generation := time.Now().UTC()
			if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningBackup, Timestamp: generation.Add(-time.Second)}, time.Second); err != nil {
				t.Fatal(err)
			}
			host := "backup"
			if test.legacy {
				host = ""
			}
			if err := node.Apply(run.StackStateChanged{StackID: "web", From: run.StateRunningBackup, To: run.StateStopping, Event: run.EventStopStack, NodeID: host, Timestamp: generation}, time.Second); err != nil {
				t.Fatal(err)
			}
			if test.restart {
				node, err = floatraft.NewLocalNode(ctx, db, "backup", zap.NewNop())
				if err != nil {
					t.Fatal(err)
				}
			}
			pool := hostclient.NewPool(zap.NewNop())
			defer pool.Close()
			var primaryCalls, backupCalls atomic.Int32
			for _, host := range []string{"primary", "backup"} {
				socket := filepath.Join(t.TempDir(), host+".sock")
				server := ipc.NewServer(socket, zap.NewNop())
				server.Handle("probe", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
				server.Handle("compose.down", func(context.Context, json.RawMessage) (any, error) {
					if host == "primary" {
						primaryCalls.Add(1)
						return nil, nil
					}
					if backupCalls.Add(1) == 1 {
						return nil, errors.New("definite down failure")
					}
					return nil, nil
				})
				go func() { _ = server.Run(ctx) }()
				pool.Register(host, socket)
				deadline := time.Now().Add(3 * time.Second)
				for {
					if _, err := pool.Execute(ctx, host, "probe", nil); err == nil {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("host did not start")
					}
					time.Sleep(time.Millisecond)
				}
			}
			orch := New(cfg, node, pool, db, zap.NewNop())
			if err := orch.ops.Create(ctx, operation.Operation{ID: "first", StackID: "web", Action: "stop", State: "running", CreatedAt: generation, UpdatedAt: generation}); err != nil {
				t.Fatal(err)
			}
			orch.doStop(ctx, "web", generation)
			state, _ := node.FSM().State("web")
			if state.State != run.StateFailed || primaryCalls.Load() != 0 || backupCalls.Load() != 1 {
				t.Fatalf("wrong first stop: state=%+v primary=%d backup=%d", state, primaryCalls.Load(), backupCalls.Load())
			}
			node, err = floatraft.NewLocalNode(ctx, db, "backup", zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			orch = New(cfg, node, pool, db, zap.NewNop())
			state, _ = node.FSM().State("web")
			retryAt := generation.Add(time.Second)
			if err := orch.ops.Create(ctx, operation.Operation{ID: "retry", StackID: "web", Action: "stop", State: "running", CreatedAt: retryAt, UpdatedAt: retryAt}); err != nil {
				t.Fatal(err)
			}
			if err := node.Apply(run.StackStateChanged{StackID: "web", From: run.StateFailed, ExpectedUpdatedAt: state.UpdatedAt, To: run.StateStopping, Event: run.EventStopStack, Timestamp: retryAt}, time.Second); err != nil {
				t.Fatal(err)
			}
			orch.doStop(ctx, "web", retryAt)
			state, _ = node.FSM().State("web")
			if state.State != run.StateIdle || primaryCalls.Load() != 0 || backupCalls.Load() != 2 {
				t.Fatalf("wrong retried stop: state=%+v primary=%d backup=%d", state, primaryCalls.Load(), backupCalls.Load())
			}
		})
	}
}
