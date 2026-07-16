package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/floatlab/floatlab-core/internal/agent"
)

func main() {
	var cfg agent.Config

	flag.StringVar(&cfg.SocketPath, "socket", "/run/floatlab/agent.sock", "Unix socket path exposed to the FloatLab API server")
	flag.StringVar(&cfg.DockerSocketPath, "docker-socket", "/var/run/docker.sock", "Docker Engine Unix socket path")
	flag.StringVar(&cfg.VictoriaLogsURL, "victoria-logs-url", "http://127.0.0.1:9428", "VictoriaLogs base URL")
	flag.BoolVar(&cfg.ForwardDmesg, "forward-dmesg", true, "Forward kernel dmesg output to VictoriaLogs")
	flag.StringVar(&cfg.NodeID, "node-id", "", "Stable FloatLab node identifier for metrics")
	flag.StringVar(&cfg.VictoriaMetricsURL, "victoria-metrics-url", "http://127.0.0.1:8428", "VictoriaMetrics base URL")
	flag.DurationVar(&cfg.MetricsInterval, "metrics-interval", 5*time.Second, "Metrics collection interval")
	flag.BoolVar(&cfg.ForwardMetrics, "forward-metrics", true, "Forward ZFS and disk metrics to VictoriaMetrics")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	svc := agent.NewService(cfg, logger)
	if err := svc.Bootstrap(ctx); err != nil {
		logger.Error("bootstrap failed", "err", err)
		os.Exit(1)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Serve(ctx)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := svc.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "err", err)
		}
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("serve failed", "err", err)
			os.Exit(1)
		}
	}
}
