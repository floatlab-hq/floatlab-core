package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func TestStackDeletePurgesOwnedRecoveryTrees(t *testing.T) {
	for _, scenario := range []struct {
		name, blocked, dependency string
		purge                     bool
	}{
		{name: "retain"},
		{name: "purge", purge: true},
		{name: "newer-depends-on-older", purge: true, blocked: "restore-newer", dependency: "restore-older"},
		{name: "older-depends-on-newer", purge: true, blocked: "restore-older", dependency: "restore-newer"},
		{name: "foreign-clone-blocks-purge", purge: true, blocked: "restore-older", dependency: "restore-foreign"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "delete.sqlite"), "node")
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
			store := config.NewStore(db)
			if err := store.CreateStack(ctx, &config.Stack{ID: "app", Name: "app", ZFSDataset: "floatlab/app", ComposeYAML: "services: {}"}); err != nil {
				t.Fatal(err)
			}
			ops := operation.NewStore(db)
			for _, op := range []operation.Operation{
				{ID: "restore-older", StackID: "app", Action: "restore", State: "succeeded"},
				{ID: "restore-newer", StackID: "app", Action: "restore", State: "succeeded"},
				{ID: "restore-foreign", StackID: "another", Action: "restore", State: "succeeded"},
				{ID: "delete", StackID: "app", Action: "delete", State: "pending"},
			} {
				if err := ops.Create(ctx, op); err != nil {
					t.Fatal(err)
				}
			}
			dir, err := os.MkdirTemp("/tmp", "delete-worker-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "host.sock")
			daemon := ipc.NewServer(socket, zap.NewNop())
			for _, command := range []string{"ping", "compose.down", "net.veth.delete"} {
				daemon.Handle(command, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
			}
			remaining := map[string]bool{"floatlab/app-recovery-restore-older": true, "floatlab/app-recovery-restore-newer": true, "floatlab/app-restore-restore-older": true, "floatlab/app-recovery-restore-foreign": true}
			daemon.Handle("fs.dataset.destroy", func(_ context.Context, raw json.RawMessage) (any, error) {
				var p ipc.DatasetDestroyPayload
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				if !p.Recursive {
					return nil, errors.New("purge must include descendants")
				}
				if p.Dataset == "floatlab/app-recovery-"+scenario.blocked && remaining["floatlab/app-recovery-"+scenario.dependency] {
					return nil, errors.New("snapshot has dependent clone")
				}
				if p.Dataset == "floatlab/app" && (remaining["floatlab/app-recovery-restore-older"] || remaining["floatlab/app-recovery-restore-newer"] || remaining["floatlab/app-restore-restore-older"]) {
					return nil, errors.New("active snapshots have dependent recovery clones")
				}
				delete(remaining, p.Dataset)
				return nil, nil
			})
			done := make(chan error, 1)
			go func() { done <- daemon.Run(ctx) }()
			defer func() { cancel(); <-done }()
			pool := hostclient.NewPool(zap.NewNop())
			defer pool.Close()
			pool.Register("node", socket)
			deadline := time.Now().Add(time.Second)
			for {
				if _, err := pool.Execute(ctx, "node", "ping", nil); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("IPC did not start")
				}
				time.Sleep(time.Millisecond)
			}
			w := &Worker{db: db, store: store, ops: ops, pool: pool}
			raw, _ := json.Marshal(StackDeletePayload{OperationID: "delete", StackID: "app", NodeID: "node", DatasetPath: "floatlab/app", Purge: scenario.purge})
			err = w.handleStackDelete(ctx, raw)
			if scenario.dependency == "restore-foreign" {
				if err == nil || !remaining["floatlab/app-recovery-restore-foreign"] || !remaining["floatlab/app-recovery-restore-older"] {
					t.Fatalf("foreign clone was not protected: %v, remaining %v", err, remaining)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{"floatlab/app-recovery-restore-foreign": true}
			if !scenario.purge {
				want["floatlab/app-recovery-restore-older"] = true
				want["floatlab/app-recovery-restore-newer"] = true
				want["floatlab/app-restore-restore-older"] = true
			}
			if !reflect.DeepEqual(remaining, want) {
				t.Fatalf("remaining datasets = %v; want %v", remaining, want)
			}
		})
	}
}
