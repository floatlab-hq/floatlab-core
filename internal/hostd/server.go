package hostd

import (
	"context"
	"fmt"
	"os"

	"github.com/floatlab/floatlab-core/pkg/docker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

const socketPath = "/run/floatlab/hostd.sock"

// Server is the host daemon IPC server.
// It is intentionally dumb: no business logic, only command execution.
type Server struct {
	ipc        *ipc.Server
	dispatcher *Dispatcher
	restore    *RestoreRunner
	log        *zap.Logger
}

func NewServer(log *zap.Logger) *Server {
	srv := &Server{log: log}
	srv.ipc = ipc.NewServer(socketPath, log)

	dc, err := docker.New()
	if err != nil {
		log.Warn("hostd: docker client unavailable, container management will fail",
			zap.Error(err))
	}

	srv.dispatcher = newDispatcher(srv.ipc, dc, log)
	srv.restore = newRestoreRunner(srv.ipc, log)
	return srv
}

func (s *Server) Run(ctx context.Context) error {
	if err := os.MkdirAll("/run/floatlab", 0750); err != nil {
		return fmt.Errorf("hostd: mkdir /run/floatlab: %w", err)
	}

	// Register all command handlers on the IPC server.
	s.dispatcher.register()

	// Emit hostd.ready after the socket is bound.
	// The restore runner fires after IPC is ready.
	go func() {
		// Brief wait for IPC server to start accepting before emitting ready.
		s.ipc.Emit("hostd.ready", map[string]interface{}{
			"hostname":       hostname(),
			"uptime_seconds": 0,
		})
		// Restore core services on boot.
		if err := s.restore.Run(ctx); err != nil {
			s.log.Error("hostd: restore failed", zap.Error(err))
		}
	}()

	return s.ipc.Run(ctx)
}

func hostname() string {
	h, _ := os.Hostname()
	return h
}
