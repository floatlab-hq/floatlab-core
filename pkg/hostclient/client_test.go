package hostclient

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

func TestCancelledProbePreservesConcurrentHostOperation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket := filepath.Join(t.TempDir(), "host.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	probeEntered, operationEntered, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(finish)
	server.Handle("sys.version", func(context.Context, json.RawMessage) (any, error) { close(probeEntered); <-finish; return nil, nil })
	server.Handle("operation", func(context.Context, json.RawMessage) (any, error) {
		close(operationEntered)
		<-finish
		return "done", nil
	})
	go func() { _ = server.Run(ctx) }()
	pool := NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("node", socket)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := pool.getOrConnect(ctx, "node"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server did not start")
		}
		time.Sleep(time.Millisecond)
	}
	probeCtx, stopProbe := context.WithCancel(ctx)
	probeResult := make(chan error, 1)
	go func() { _, err := pool.Execute(probeCtx, "node", "sys.version", nil); probeResult <- err }()
	<-probeEntered
	operationCtx, stopOperation := context.WithTimeout(ctx, time.Second)
	defer stopOperation()
	operationResult := make(chan error, 1)
	go func() { _, err := pool.Execute(operationCtx, "node", "operation", nil); operationResult <- err }()
	<-operationEntered
	stopProbe()
	if err := <-probeResult; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Both requests must still share the original, healthy connection.
	pool.mu.RLock()
	connected := pool.clients["node"] != nil
	pool.mu.RUnlock()
	if !connected {
		t.Fatal("cancelled probe discarded shared connection")
	}
	finish <- struct{}{}
	finish <- struct{}{}
	if err := <-operationResult; err != nil {
		t.Fatalf("advisory probe interrupted operation: %v", err)
	}
}
