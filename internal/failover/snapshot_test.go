package failover

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
	"go.uber.org/zap"
)

func TestFinalSyncUsesSharedAutomatedSnapshotNames(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := filepath.Join(t.TempDir(), "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	var snapshot ipc.SnapshotCreatePayload
	server.Handle("fs.snapshot.create", func(_ context.Context, raw json.RawMessage) (any, error) { return nil, json.Unmarshal(raw, &snapshot) })
	server.Handle("fs.repl.send", func(_ context.Context, raw json.RawMessage) (any, error) {
		var send ipc.ReplSendPayload
		if err := json.Unmarshal(raw, &send); err != nil {
			return nil, err
		}
		if send.Snapshot != snapshot.Name {
			t.Errorf("send snapshot %q differs from created %q", send.Snapshot, snapshot.Name)
		}
		return nil, nil
	})
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx) }()
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
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("primary", socket)
	sequence := &Sequence{hosts: pool}
	if err := sequence.attemptFinalSync(ctx, &config.Stack{ID: "stack", PrimaryNodeID: "primary", BackupNodeID: "backup", ZFSDataset: "floatlab/app"}); err != nil {
		t.Fatal(err)
	}
	if !store.IsAutomatedSnapshot(snapshot.Name) || snapshot.Properties["floatlab:purpose"] != "replication" || snapshot.Properties["floatlab:operation-id"] == "" {
		t.Fatalf("final sync lacks canonical naming and metadata: %+v", snapshot)
	}
}
