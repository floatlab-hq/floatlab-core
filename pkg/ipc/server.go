package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"

	"go.uber.org/zap"
)

// Handler processes a command and returns a response payload or an error.
type Handler func(ctx context.Context, payload json.RawMessage) (any, error)

type connectionContextKey struct{}

// ConnectionContext is for subscriptions that outlive their opening command.
// It is cancelled when the client disconnects or the daemon stops.
func ConnectionContext(ctx context.Context) context.Context {
	if connection, ok := ctx.Value(connectionContextKey{}).(context.Context); ok {
		return connection
	}
	return ctx
}

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
	s.EmitContext(context.Background(), name, payload)
}

func (s *Server) EmitContext(ctx context.Context, name string, payload any) {
	b, _ := json.Marshal(payload)
	select {
	case s.events <- Event{Name: name, Payload: json.RawMessage(b)}:
	case <-ctx.Done():
	}
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
	ctx, cancel := context.WithCancel(ctx)
	ctx = context.WithValue(ctx, connectionContextKey{}, ctx)
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	var commandsMu sync.Mutex
	commands := make(map[string]context.CancelFunc)
	defer func() {
		cancel()
		c.Close()
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
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
			if msg.Type == TypeCancel {
				commandsMu.Lock()
				if cancel := commands[msg.ID]; cancel != nil {
					cancel()
				}
				commandsMu.Unlock()
			}
			continue
		}
		var cmd Command
		if err := json.Unmarshal(msg.Payload, &cmd); err != nil {
			s.log.Warn("ipc: bad command payload", zap.Error(err))
			continue
		}
		commandCtx, stopCommand := context.WithCancel(ctx)
		if cmd.Deadline != nil {
			stopCommand()
			commandCtx, stopCommand = context.WithDeadline(ctx, *cmd.Deadline)
		}
		commandsMu.Lock()
		if commands[msg.ID] != nil {
			commandsMu.Unlock()
			stopCommand()
			continue
		}
		commands[msg.ID] = stopCommand
		commandsMu.Unlock()
		go func() {
			defer stopCommand()
			defer func() {
				commandsMu.Lock()
				delete(commands, msg.ID)
				commandsMu.Unlock()
			}()
			s.dispatch(commandCtx, c, msg.ID, cmd)
		}()
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
	if ConnectionContext(ctx).Err() != nil {
		// A daemon shutdown makes the outcome uncertain even if the command returns first.
		return
	}
	var resp Message
	resp.ID = id
	resp.Type = TypeResponse

	if err != nil {
		rErr := &RPCError{Code: "cmd.error", Message: err.Error()}
		var typed *RPCError
		if errors.As(err, &typed) {
			rErr = typed
		}
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
