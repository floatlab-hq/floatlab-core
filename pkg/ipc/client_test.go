package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
)

func connectedClient(t *testing.T, server *Server) *Client {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	client := NewClient("")
	client.conn = NewConn(clientSide)
	go client.readLoop(client.conn, client.done)
	ctx, cancel := context.WithCancel(context.Background())
	go server.serveClient(ctx, NewConn(serverSide))
	t.Cleanup(func() { cancel(); _ = client.Close() })
	return client
}

func TestCancellationWaitsForHandlerExitWithoutInterruptingOtherCommands(t *testing.T) {
	server := NewServer("", zap.NewNop())
	entered, cancelling, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(release)
	server.Handle("mutate", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelling)
		// Simulate bounded cleanup before a mutation is safe to retry.
		<-release
		return nil, ctx.Err()
	})
	server.Handle("read", func(context.Context, json.RawMessage) (any, error) { return "healthy", nil })
	client := connectedClient(t, server)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Execute(ctx, "mutate", nil); result <- err }()
	<-entered
	cancel()
	select {
	case <-cancelling:
	case <-time.After(time.Second):
		t.Fatal("handler never received cancellation")
	}
	select {
	case err := <-result:
		t.Fatalf("returned before mutation cleanup finished: %v", err)
	default:
	}
	readCtx, stopRead := context.WithTimeout(context.Background(), time.Second)
	defer stopRead()
	if _, err := client.Execute(readCtx, "read", nil); err != nil {
		t.Fatalf("cancelling a mutation interrupted shared connection: %v", err)
	}
	release <- struct{}{}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler exit did not acknowledge cancellation")
	}
}

func TestCallerDeadlineReachesHandler(t *testing.T) {
	server := NewServer("", zap.NewNop())
	deadlineSeen := make(chan time.Time, 1)
	server.Handle("mutate", func(ctx context.Context, _ json.RawMessage) (any, error) {
		deadline, _ := ctx.Deadline()
		deadlineSeen <- deadline
		<-ctx.Done()
		return nil, ctx.Err()
	})
	client := connectedClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Execute(ctx, "mutate", nil); result <- err }()
	want, _ := ctx.Deadline()
	select {
	case got := <-deadlineSeen:
		if !got.Equal(want) {
			t.Fatalf("handler deadline %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not receive command")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not finish")
	}
}

func TestDisconnectCancelsHandlerAndUnblocksWaiters(t *testing.T) {
	server := NewServer("", zap.NewNop())
	entered, cancelled := make(chan struct{}), make(chan struct{})
	server.Handle("mutate", func(ctx context.Context, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return nil, ctx.Err()
	})
	client := connectedClient(t, server)
	result := make(chan error, 1)
	go func() { _, err := client.Execute(context.Background(), "mutate", nil); result <- err }()
	<-entered
	done := client.Done()
	_ = client.Close()
	select {
	case err := <-result:
		if !errors.Is(err, ErrOutcomeUnknown) || !strings.Contains(err.Error(), "outcome unknown") {
			t.Fatalf("disconnect outcome: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("connection loss left caller blocked")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("connection loss did not cancel handler")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection loss did not notify subscribers")
	}
}

func TestBlockedWriteHonorsCancellation(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := NewClient("")
	client.conn = NewConn(clientSide)
	go client.readLoop(client.conn, client.done)
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.Execute(ctx, "mutate", nil); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("write to unresponsive peer unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked write ignored caller deadline")
	}
	closed := make(chan struct{})
	go func() { _ = client.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("blocked write held the client mutex and prevented Close")
	}
}

func TestSubscriptionContextSurvivesResponseAndEndsOnDisconnect(t *testing.T) {
	server := NewServer("", zap.NewNop())
	lifetime := make(chan context.Context, 1)
	server.Handle("subscribe", func(ctx context.Context, _ json.RawMessage) (any, error) {
		lifetime <- ConnectionContext(ctx)
		return nil, nil
	})
	client := connectedClient(t, server)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	if _, err := client.Execute(ctx, "subscribe", nil); err != nil {
		t.Fatal(err)
	}
	cancel()
	connection := <-lifetime
	if err := connection.Err(); err != nil {
		t.Fatalf("command completion ended subscription: %v", err)
	}
	_ = client.Close()
	select {
	case <-connection.Done():
	case <-time.After(time.Second):
		t.Fatal("disconnect leaked subscription context")
	}
}
