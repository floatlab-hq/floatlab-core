package worker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func TestRestoreDatasetsActivatesSnapshotMountsAcrossRetries(t *testing.T) {
	for _, failAt := range []string{"", "floatlab/app-recovery-op", "floatlab/app-recovery-op/data", "floatlab/app", "floatlab/app/data", "starting", "phase-activating-snapshot", "phase-starting"} {
		t.Run("mount-failure-"+failAt, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "restore.sqlite"), "node")
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
			if err := ops.Create(ctx, operation.Operation{ID: "op", StackID: "app", State: "pending", Checkpoint: "accepted", CreatedAt: time.Now().UTC()}); err != nil {
				t.Fatal(err)
			}
			phase := ""
			if strings.HasPrefix(failAt, "phase-") {
				phase = strings.TrimPrefix(failAt, "phase-")
				if err := db.Execute(ctx, []rqlite.Statement{{SQL: "CREATE TRIGGER reject_phase BEFORE UPDATE OF checkpoint ON operations WHEN NEW.checkpoint='" + phase + "' BEGIN SELECT RAISE(ABORT, 'checkpoint write rejected'); END"}}); err != nil {
					t.Fatal(err)
				}
			}
			type dataset struct{ mountpoint, marker string }
			datasets := map[string]dataset{
				"floatlab/app":      {"/floatlab/app", "changed-marker"},
				"floatlab/app/data": {"/floatlab/app/data", "changed-marker"},
			}
			dir, err := os.MkdirTemp("/tmp", "restore-mount-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "host.sock")
			daemon := ipc.NewServer(socket, zap.NewNop())
			failed, running := false, true
			snapshots := 0
			daemon.Handle("fs.snapshot.create", func(context.Context, json.RawMessage) (any, error) {
				snapshots++
				if _, exists := datasets["floatlab/app"]; !exists {
					return nil, errors.New("source dataset missing during swap")
				}
				return nil, nil
			})
			daemon.Handle("compose.source.write", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
			daemon.Handle("compose.down", func(context.Context, json.RawMessage) (any, error) {
				if _, exists := datasets["floatlab/app"]; !exists {
					return nil, errors.New("compose source missing during swap")
				}
				if failAt == "starting" && failed {
					op, err := ops.Get(ctx, "op")
					if err != nil || op.Checkpoint != "starting" {
						return nil, errors.New("starting replay lost durable checkpoint before stopping")
					}
				}
				running = false
				return nil, nil
			})
			daemon.Handle("compose.up", func(context.Context, json.RawMessage) (any, error) {
				running = true
				if failAt == "starting" && !failed {
					failed = true
					return nil, errors.New("lost reply after successful compose up")
				}
				return nil, nil
			})
			daemon.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) {
				return ipc.DockerListResult{Containers: []ipc.ContainerInfo{{State: "running", Health: "none"}}}, nil
			})
			daemon.Handle("fs.dataset.list", func(context.Context, json.RawMessage) (any, error) {
				var names []string
				for name := range datasets {
					names = append(names, name)
				}
				sort.Strings(names)
				if _, swapping := datasets["floatlab/app-recovery-op"]; swapping {
					sort.Sort(sort.Reverse(sort.StringSlice(names)))
				}
				result := ipc.DatasetListResult{}
				for _, name := range names {
					result.Datasets = append(result.Datasets, ipc.DatasetInfoResult{Name: name})
				}
				return result, nil
			})
			daemon.Handle("fs.dataset.clone", func(_ context.Context, raw json.RawMessage) (any, error) {
				var p ipc.DatasetClonePayload
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				if _, exists := datasets[p.Target]; !exists {
					datasets[p.Target] = dataset{"none", "persistent-marker"}
				}
				return nil, nil
			})
			daemon.Handle("fs.dataset.rename", func(_ context.Context, raw json.RawMessage) (any, error) {
				var p ipc.DatasetRenamePayload
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				for name, value := range datasets {
					if name == p.Source || strings.HasPrefix(name, p.Source+"/") {
						delete(datasets, name)
						datasets[p.Target+strings.TrimPrefix(name, p.Source)] = value
					}
				}
				return nil, nil
			})
			daemon.Handle("fs.dataset.set", func(_ context.Context, raw json.RawMessage) (any, error) {
				var p ipc.DatasetSetPayload
				if err := json.Unmarshal(raw, &p); err != nil {
					return nil, err
				}
				if running {
					return nil, errors.New("mountpoint busy: workload still running")
				}
				if p.Dataset == failAt && !failed {
					failed = true
					return nil, errors.New("injected mountpoint interruption")
				}
				value, exists := datasets[p.Dataset]
				if !exists {
					return nil, errors.New("dataset missing")
				}
				mountpoint := p.Properties["mountpoint"]
				if slash := strings.LastIndex(p.Dataset, "/"); slash > 0 && mountpoint != "none" {
					if parent, exists := datasets[p.Dataset[:slash]]; exists && parent.mountpoint == "none" {
						return nil, errors.New("parent dataset must mount before child")
					}
				}
				if value.mountpoint != "none" {
					for name, other := range datasets {
						if name != p.Dataset && strings.HasPrefix(other.mountpoint, value.mountpoint+"/") {
							return nil, errors.New("cannot unmount parent: explicit child dataset is mounted")
						}
					}
				}
				for name, other := range datasets {
					if name != p.Dataset && mountpoint != "none" && other.mountpoint == mountpoint {
						return nil, errors.New("recovery dataset still occupies activation mountpoint")
					}
				}
				value.mountpoint = mountpoint
				datasets[p.Dataset] = value
				return nil, nil
			})
			daemon.Handle("fs.dataset.promote", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
			done := make(chan error, 1)
			go func() { done <- daemon.Run(ctx) }()
			defer func() { cancel(); <-done }()
			pool := hostclient.NewPool(zap.NewNop())
			defer pool.Close()
			pool.Register("node", socket)
			deadline := time.Now().Add(time.Second)
			for {
				if _, err := os.Stat(socket); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("IPC did not start")
				}
				time.Sleep(time.Millisecond)
			}
			w := &Worker{pool: pool, db: db, store: store, ops: ops}
			raw, _ := json.Marshal(StackRestorePayload{OperationID: "op", StackID: "app", NodeID: "node", DatasetPath: "floatlab/app", Snapshot: "snapshot", SourceCompose: "services: {}", WasRunning: true})
			err = w.handleStackRestore(ctx, raw)
			if phase != "" {
				if err == nil || !strings.Contains(err.Error(), "checkpoint write rejected") {
					t.Fatalf("restore mutated past failed %s checkpoint: %v", phase, err)
				}
				if running {
					t.Fatal("workload started before durable checkpoint")
				}
				_, swapped := datasets["floatlab/app-recovery-op"]
				if swapped != (phase == "starting") {
					t.Fatalf("swap crossed failed %s checkpoint: recovery tree exists=%v", phase, swapped)
				}
				if err := db.Execute(ctx, []rqlite.Statement{{SQL: "DROP TRIGGER reject_phase"}}); err != nil {
					t.Fatal(err)
				}
				err = w.handleStackRestore(ctx, raw)
			} else if failAt != "" {
				if err == nil || !failed {
					t.Fatalf("mount interruption not observed: %v", err)
				}
				err = w.handleStackRestore(ctx, raw)
			}
			if err != nil {
				t.Fatal(err)
			}
			wantSnapshots := 1
			if phase == "activating-snapshot" {
				wantSnapshots = 2
			}
			if snapshots != wantSnapshots {
				t.Fatalf("snapshot preservation repeated after swap: %d calls", snapshots)
			}
			for name, value := range datasets {
				if value.mountpoint != "/"+name {
					t.Fatalf("dataset %s has mountpoint %s", name, value.mountpoint)
				}
				if strings.HasPrefix(name, "floatlab/app/data") && value.marker != "persistent-marker" {
					t.Fatalf("persistent data: %q want persistent-marker", value.marker)
				}
			}
			if len(datasets) != 4 {
				t.Fatalf("restore replay created extra trees: %v", datasets)
			}
		})
	}
}
