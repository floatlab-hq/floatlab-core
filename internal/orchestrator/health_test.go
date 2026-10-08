package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestDelayedStopEventDoesNotFailHealthyReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(db)
	if err := store.CreateStack(ctx, &config.Stack{ID: "stack", Name: "test", PrimaryNodeID: "node"}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	node, err := floatraft.NewNode(floatraft.Config{NodeID: "test", BindAddr: address, AdvertiseAddr: address, DataDir: t.TempDir(), Bootstrap: true}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer node.Shutdown()
	deadline := time.Now().Add(3 * time.Second)
	for !node.IsLeader() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !node.IsLeader() {
		t.Fatal("raft leader not ready")
	}
	socket := filepath.Join(t.TempDir(), "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	var listing atomic.Value
	listing.Store(ipc.DockerListResult{})
	server.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) { return listing.Load(), nil })
	go server.Run(ctx)
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("node", socket)
	for {
		if _, err := pool.Execute(ctx, "node", "docker.list", ipc.DockerListPayload{StackID: "stack"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	orch := New(store, node, pool, db, zap.NewNop())
	for _, test := range []struct {
		name, id, state string
		want            run.State
	}{
		{"removed old container", "replacement", "running", run.StateRunningPrimary},
		{"same container restarted", "old", "running", run.StateRunningPrimary},
		{"current container exited", "old", "exited", run.StateFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			listing.Store(ipc.DockerListResult{Containers: []ipc.ContainerInfo{{ID: test.id, State: test.state, StackID: "stack"}}})
			if err := node.Apply(run.StackStateChanged{StackID: "stack", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
				t.Fatal(err)
			}
			orch.handleContainerState(ctx, ipc.ContainerStateEvent{StackID: "stack", ContainerID: "old", Status: "die"})
			instance, _ := node.FSM().State("stack")
			if instance.State != test.want {
				t.Fatalf("state %s want %s", instance.State, test.want)
			}
		})
	}
}

func TestControlRestartDoesNotRedispatchCommittedHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := testdb.Start(t)
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	store := config.NewStore(db)
	source := "name: restored\nx-fl-stack:\n  schema_version: 1\n  primary_node: node\n  failover: {mode: manual}\n  storage: {pool: floatlab}\nservices:\n  app: {image: 'alpine:3.20'}\n"
	if err := store.CreateStack(ctx, &config.Stack{ID: "restored", Name: "restored", PrimaryNodeID: "node", ZFSDataset: "floatlab/restored", ComposeYAML: source}); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	makeNode := func() *floatraft.Node {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		node, err := floatraft.NewNode(floatraft.Config{NodeID: "test", BindAddr: address, AdvertiseAddr: address, DataDir: dataDir, Bootstrap: true}, zap.NewNop())
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	waitLeader := func(node *floatraft.Node) {
		deadline := time.Now().Add(3 * time.Second)
		for !node.IsLeader() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if !node.IsLeader() {
			t.Fatal("raft leader not ready")
		}
		if err := node.Barrier(time.Second); err != nil {
			t.Fatal(err)
		}
	}
	original := makeNode()
	waitLeader(original)
	for _, state := range []run.State{run.StateProvisioning, run.StateIdle, run.StateStarting, run.StateRunningPrimary} {
		if err := original.Apply(run.StackStateChanged{StackID: "restored", To: state, Timestamp: time.Now().UTC()}, time.Second); err != nil {
			t.Fatal(err)
		}
	}
	if err := original.Shutdown(); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "restore.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	var calls atomic.Int32
	for _, name := range []string{"fs.dataset.create", "compose.up"} {
		server.Handle(name, func(context.Context, json.RawMessage) (any, error) {
			calls.Add(1)
			return nil, fmt.Errorf("historical action was redispatched")
		})
	}
	go server.Run(ctx)
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("node", socket)
	restored := makeNode()
	defer restored.Shutdown()
	orch := New(store, restored, pool, db, zap.NewNop())
	done := make(chan error, 1)
	go func() { done <- orch.Run(ctx) }()
	waitLeader(restored)
	time.Sleep(200 * time.Millisecond) // Allow a wrongly replayed action to reach the IPC handler.
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("orchestrator: %v", err)
	}
	state, ok := restored.FSM().State("restored")
	if !ok || state.State != run.StateRunningPrimary || calls.Load() != 0 {
		t.Fatalf("restart replay: state=%+v commands=%d", state, calls.Load())
	}
}
