package failover

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestFailoverRemainsExclusiveAndAbortableThroughTakeoverAndStart(t *testing.T) {
	for _, blockedCommand := range []string{"net.addr.add", "compose.up"} {
		t.Run(blockedCommand, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "primary")
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
			if err := cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "primary", BackupNodeID: "backup", ZFSDataset: "floatlab/web", ComposeYAML: "services: {}"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO ip_reservations(id,stack_id,service,address,prefix_pool,allocated_at) VALUES('address','web','web','fd00::1/64','legacy',?)`, Params: []any{time.Now().UTC()}}}); err != nil {
				t.Fatal(err)
			}
			node, err := floatraft.NewLocalNode(ctx, db, "primary", zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(t.TempDir(), "host.sock")
			daemon := ipc.NewServer(socket, zap.NewNop())
			daemon.Handle("fs.snapshot.create", func(context.Context, json.RawMessage) (any, error) {
				return nil, errors.New("primary unavailable; use existing backup")
			})
			entered := make(chan struct{})
			for _, command := range []string{"net.addr.add", "compose.up"} {
				daemon.Handle(command, func(ctx context.Context, _ json.RawMessage) (any, error) {
					if command == blockedCommand {
						close(entered)
						<-ctx.Done()
						return nil, ctx.Err()
					}
					return nil, nil
				})
			}
			serverDone := make(chan error, 1)
			go func() { serverDone <- daemon.Run(ctx) }()
			defer func() { cancel(); <-serverDone }()
			for {
				if _, err := os.Stat(socket); err == nil {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("IPC listener did not start")
				case <-time.After(time.Millisecond):
				}
			}
			hosts := hostclient.NewPool(zap.NewNop())
			defer hosts.Close()
			hosts.Register("primary", socket)
			hosts.Register("backup", socket)
			sequence := NewSequence(db, cfg, node, hosts, nil, zap.NewNop())
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); result <- sequence.Execute(ctx, "web") }()
			defer func() { cancel(); <-finished }()
			select {
			case <-entered:
			case err := <-result:
				t.Fatalf("sequence failed before %s: %v", blockedCommand, err)
			case <-ctx.Done():
				t.Fatal("sequence did not reach blocked command")
			}
			if err := sequence.Execute(ctx, "web"); err == nil || !strings.Contains(err.Error(), "already active") {
				t.Errorf("concurrent sequence bypassed active guard: %v", err)
			}
			if !sequence.Abort("web") {
				t.Fatal("active failover stopped being abortable before its sequence completed")
			}
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("abort outcome: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("abort did not stop the blocked command")
			}
			if sequence.Abort("web") {
				t.Fatal("completed sequence retained its active guard")
			}
			instance, _ := node.FSM().State("web")
			if instance.State != run.StateFailed {
				t.Fatalf("aborted sequence state %s, want Failed", instance.State)
			}
		})
	}
}
