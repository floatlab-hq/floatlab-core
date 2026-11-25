package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestWebsocketVNCTransfersInBothDirections(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.WriteString(conn, "RFB 003.008\n")
		response := make([]byte, 3)
		if _, err := io.ReadFull(conn, response); err == nil && string(response) == "ack" {
			_, _ = io.WriteString(conn, "ok")
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vnc(w, r, target.Addr().String())
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/websockify",
		&websocket.DialOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	kind, handshake, err := conn.Read(ctx)
	if err != nil || kind != websocket.MessageBinary || string(handshake) != "RFB 003.008\n" {
		t.Fatalf("handshake = %q, type = %v, error = %v", handshake, kind, err)
	}
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("ack")); err != nil {
		t.Fatal(err)
	}
	_, response, err := conn.Read(ctx)
	if err != nil || string(response) != "ok" {
		t.Fatalf("response = %q, error = %v", response, err)
	}
}
