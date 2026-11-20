package hostclient

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

type delayedPayload struct {
	entered chan struct{}
	release chan struct{}
}

func (p delayedPayload) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return []byte(`{}`), nil
}

func TestStaleFailurePreservesReplacementConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket := filepath.Join(t.TempDir(), "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	server.Handle("probe", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
	go func() { _ = server.Run(ctx) }()
	pool := NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("node", socket)
	for {
		if _, err := pool.Execute(ctx, "node", "probe", nil); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("IPC server did not start")
		case <-time.After(time.Millisecond):
		}
	}

	payload := delayedPayload{entered: make(chan struct{}), release: make(chan struct{})}
	failed := make(chan error, 1)
	go func() {
		_, err := pool.Execute(ctx, "node", "probe", payload)
		failed <- err
	}()
	select {
	case <-payload.entered:
	case <-ctx.Done():
		t.Fatal("old request did not capture its connection")
	}
	// Replace the transport while the old call still owns its original client.
	pool.Register("node", socket)
	if _, err := pool.Execute(ctx, "node", "probe", nil); err != nil {
		close(payload.release)
		t.Fatalf("connect replacement: %v", err)
	}
	pool.mu.RLock()
	replacement := pool.clients["node"]
	pool.mu.RUnlock()
	close(payload.release)
	select {
	case err := <-failed:
		if err == nil {
			t.Fatal("expected old transport to fail")
		}
	case <-ctx.Done():
		t.Fatal("old request did not finish")
	}
	pool.mu.RLock()
	current := pool.clients["node"]
	pool.mu.RUnlock()
	if current != replacement {
		t.Fatal("stale failure evicted the replacement connection")
	}
	if _, err := pool.Execute(ctx, "node", "probe", nil); err != nil {
		t.Fatalf("replacement is unusable: %v", err)
	}
}
