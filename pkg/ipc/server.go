package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"

	"go.uber.org/zap"
)

// Handler processes a command and returns a response payload or an error.
type Handler func(ctx context.Context, payload json.RawMessage) (any, error)

// Server listens on a UNIX socket and dispatches commands to registered handlers.
type Server struct {
	socketPath string
	log        *zap.Logger
	handlers   map[string]Handler
	events     chan Event
	mu         sync.RWMutex
	clients    map[*Conn]struct{}
}

func NewServer(socketPath string, log *zap.Logger) *Server {
	return &Server{
		socketPath: socketPath,
		log:        log,
		handlers:   make(map[string]Handler),
		events:     make(chan Event, 256),
		clients:    make(map[*Conn]struct{}),
	}
}

func (s *Server) Handle(name string, h Handler) {
	s.handlers[name] = h
}

// Emit broadcasts an event to all connected clients.
func (s *Server) Emit(name string, payload any) {
	b, _ := json.Marshal(payload)
	s.events <- Event{Name: name, Payload: json.RawMessage(b)}
}

func (s *Server) Run(ctx context.Context) error {
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("ipc server: remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("ipc server: listen: %w", err)
	}
	if err := os.Chmod(s.socketPath, 0660); err != nil {
		return fmt.Errorf("ipc server: chmod socket: %w", err)
	}
	defer ln.Close()
	s.log.Info("ipc server listening", zap.String("socket", s.socketPath))

	go s.broadcastEvents(ctx)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		nc, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			s.log.Error("ipc server: accept", zap.Error(err))
			continue
		}
		c := NewConn(nc)
		s.mu.Lock()
		s.clients[c] = struct{}{}
		s.mu.Unlock()
		go s.serveClient(ctx, c)
	}
}

func (s *Server) serveClient(ctx context.Context, c *Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
		c.Close()
	}()
	for {
		msg, err := c.Recv()
		if err != nil {
			if ctx.Err() == nil {
				s.log.Debug("ipc client disconnected", zap.Error(err))
			}
			return
		}
		if msg.Type != TypeCommand {
			continue
		}
		var cmd Command
		if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
			s.log.Warn("ipc: bad command payload", zap.Error(err))
			continue
		}
		go s.dispatch(ctx, c, msg.ID, cmd)
	}
}

func (s *Server) dispatch(ctx context.Context, c *Conn, id string, cmd Command) {
	h, ok := s.handlers[cmd.Name]
	if !ok {
		resp := Message{
			ID:   id,
			Type: TypeResponse,
		}
		rErr := &RPCError{Code: "cmd.unknown", Message: "unknown command: " + cmd.Name}
		b, _ := json.Marshal(Response{ID: id, OK: false, Error: rErr})
		resp.Payload = json.RawMessage(b)
		_ = c.Send(resp)
		return
	}

	result, err := h(ctx, cmd.Payload)
	var resp Message
	resp.ID = id
	resp.Type = TypeResponse

	if err != nil {
		rErr := &RPCError{Code: "cmd.error", Message: err.Error()}
		b, _ := json.Marshal(Response{ID: id, OK: false, Error: rErr})
		resp.Payload = json.RawMessage(b)
	} else {
		b, _ := json.Marshal(result)
		respData, _ := json.Marshal(Response{ID: id, OK: true, Payload: json.RawMessage(b)})
		resp.Payload = json.RawMessage(respData)
	}
	_ = c.Send(resp)
}

func (s *Server) broadcastEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.events:
			b, _ := json.Marshal(ev)
			msg := Message{Type: TypeEvent, Payload: json.RawMessage(b)}
			s.mu.RLock()
			for c := range s.clients {
				_ = c.Send(msg)
			}
			s.mu.RUnlock()
		}
	}
}
