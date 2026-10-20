package hostd

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/floatlab/floatlab-core/internal/managementupdate"

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
	updates    *managementupdate.Manager
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
	srv.updates = managementupdate.NewDefault()
	if backend, ok := srv.updates.Backend.(*managementupdate.Linux); ok {
		backend.Quiesce = srv.dispatcher.drainMutations
	}
	return srv
}

func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if err := os.MkdirAll("/run/floatlab", 0750); err != nil {
		return fmt.Errorf("hostd: mkdir /run/floatlab: %w", err)
	}

	hostUpdates := NewHostUpdateCoordinator(s.updates)
	// Register all command handlers on the IPC server.
	s.dispatcher.register()
	registerManagementUpdates(ctx, s.ipc, s.updates)
	registerHostUpdates(s.ipc, hostUpdates)
	if err := os.MkdirAll("/run/floatlab/preflight", 0750); err != nil {
		return err
	}
	preflightIPC := ipc.NewServer("/run/floatlab/preflight/hostd.sock", s.log)
	s.dispatcher.registerPreflightReads(preflightIPC)
	go func() {
		if err := preflightIPC.Run(ctx); err != nil {
			s.log.Error("preflight IPC failed", zap.Error(err))
		}
	}()
	if s.dispatcher.docker != nil {
		defer s.dispatcher.docker.Close()
	}

	// Emit hostd.ready after the socket is bound.
	// The restore runner fires after IPC is ready.
	go func() {
		for {
			conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
		if err := hostUpdates.Recover(ctx); err != nil {
			s.log.Error("host update recovery failed", zap.Error(err))
		}
		if err := s.updates.Recover(ctx); err != nil {
			s.log.Error("management update recovery failed", zap.Error(err))
		}
		if err := s.dispatcher.network.Initialize(ctx); err != nil {
			s.log.Error("network startup failed", zap.Error(err))
			return
		}
		go s.dispatcher.network.Watch(ctx, s.log)
		go hostUpdates.Supervise(ctx)
		go s.superviseTelemetry(ctx)
		s.ipc.Emit("hostd.ready", map[string]interface{}{
			"hostname":       hostname(),
			"uptime_seconds": 0,
		})
		// Restore core services on boot.
		if err := managementStartupBlocked(); err != nil {
			s.log.Warn("core restore suppressed", zap.Error(err))
			return
		}
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

func telemetryEnv(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
