package failover

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestLegacyRestoreRequiresHandoffBeforeAnyMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
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
	if err := cfg.CreateStack(ctx, &config.Stack{ID: "web", Name: "web", PrimaryNodeID: "primary", BackupNodeID: "backup", ZFSDataset: "floatlab/web", ComposeYAML: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	node, err := floatraft.NewLocalNode(ctx, db, "backup", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Apply(run.StackStateChanged{StackID: "web", To: run.StateRunningBackup, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	before, _ := node.FSM().State("web")
	socket := filepath.Join(t.TempDir(), "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	var commands atomic.Int32
	for _, name := range []string{"compose.down", "fs.snapshot.create", "fs.repl.send", "compose.up"} {
		daemon.Handle(name, func(context.Context, json.RawMessage) (any, error) {
			commands.Add(1)
			if name == "compose.down" {
				return nil, errors.New("secondary is still serving")
			}
			return nil, nil
		})
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC listener did not start")
		}
		time.Sleep(time.Millisecond)
	}
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	hosts.Register("primary", socket)
	hosts.Register("backup", socket)
	sequence := NewSequence(db, cfg, node, hosts, nil, zap.NewNop())
	if err := sequence.Restore(ctx, "web"); !errors.Is(err, ErrCoordinatedHandoffRequired) {
		t.Errorf("legacy failback was allowed without ownership/data handoff: %v", err)
	}
	if count := commands.Load(); count != 0 {
		t.Errorf("unsupported failback issued %d host commands", count)
	}
	after, _ := node.FSM().State("web")
	if *after != *before {
		t.Errorf("unsupported failback changed lifecycle: before=%+v after=%+v", before, after)
	}
}
