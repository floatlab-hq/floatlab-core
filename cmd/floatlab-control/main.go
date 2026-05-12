package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/control"
	"github.com/floatlab/floatlab-core/internal/failover"
	"github.com/floatlab/floatlab-core/internal/orchestrator"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/notify"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

var (
	listenAddr    string
	rqliteURL     string
	raftNodeID    string
	raftBindAddr  string
	raftAdvAddr   string
	raftDataDir   string
	raftBootstrap bool
	vlogsURL      string
	vmetricsURL   string
)

func main() {
	root := &cobra.Command{
		Use:   "floatlab-control",
		Short: "FloatLab control plane — state management, API, and failover orchestration",
		RunE:  run,
	}

	root.Flags().StringVar(&listenAddr, "listen", ":8080", "HTTP API listen address")
	root.Flags().StringVar(&rqliteURL, "rqlite-url", "http://localhost:4001", "rqlite base URL")
	root.Flags().StringVar(&raftNodeID, "raft-id", "node1", "Unique Raft node ID")
	root.Flags().StringVar(&raftBindAddr, "raft-bind", "0.0.0.0:7000", "Raft TCP bind address")
	root.Flags().StringVar(&raftAdvAddr, "raft-advertise", "127.0.0.1:7000", "Raft TCP advertise address (externally reachable)")
	root.Flags().StringVar(&raftDataDir, "raft-data", "/var/lib/floatlab/raft", "Raft data directory for BoltDB")
	root.Flags().BoolVar(&raftBootstrap, "raft-bootstrap", false, "Bootstrap a new single-node Raft cluster")
	root.Flags().StringVar(&vlogsURL, "vlogs-url", "http://localhost:9428", "VictoriaLogs base URL")
	root.Flags().StringVar(&vmetricsURL, "vmetrics-url", "http://localhost:8428", "VictoriaMetrics base URL")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Persistent state via rqlite.
	db := rqlite.NewClient(rqliteURL)
	if err := rqlite.Migrate(ctx, db); err != nil {
		log.Error("rqlite migrate failed", zap.Error(err))
		return err
	}

	if err := config.Migrate(ctx, db); err != nil {
		log.Error("config migrate failed", zap.Error(err))
		return err
	}
	store := config.NewStore(db)

	// Raft node.
	raftNode, err := floatraft.NewNode(floatraft.Config{
		NodeID:        raftNodeID,
		BindAddr:      raftBindAddr,
		AdvertiseAddr: raftAdvAddr,
		DataDir:       raftDataDir,
		Bootstrap:     raftBootstrap,
	}, log)
	if err != nil {
		log.Error("raft init failed", zap.Error(err))
		return err
	}
	defer raftNode.Shutdown()

	// Host client pool.
	hosts := hostclient.NewPool(log)
	defer hosts.Close()

	// Notification broker — fan-out SSE publisher.
	broker := notify.NewBroker()

	// Notification retention: purge read notifications older than 30 days, daily.
	go func() {
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := notify.Cleanup(ctx, db); err != nil {
					log.Warn("notify cleanup", zap.Error(err))
				}
			}
		}
	}()

	// Failover detector — pings primaries and auto-triggers when configured.
	seq := failover.NewSequence(db, store, raftNode, hosts, broker, log)
	detector := failover.NewDetector(store, raftNode.FSM(), hosts, seq, log)
	go detector.Run(ctx)

	// Orchestrator: drives stack state machine from Raft + IPC events.
	orch := orchestrator.New(store, raftNode, hosts, log)
	go func() {
		if err := orch.Run(ctx); err != nil && err != context.Canceled {
			log.Error("orchestrator exited", zap.Error(err))
		}
	}()

	// Snapshot scheduler: minute-tick loop that enqueues snapshot tasks.
	go orchestrator.RunScheduler(ctx, orch, db, log)

	// Task worker: polls rqlite tasks table and dispatches IPC to hostd.
	w := worker.New(db, store, hosts, log)
	go func() {
		if err := w.Run(ctx); err != nil && err != context.Canceled {
			log.Error("worker exited", zap.Error(err))
		}
	}()

	// HTTP control server.
	srv := control.NewServer(&control.Config{
		ListenAddr:  listenAddr,
		RQLiteURL:   rqliteURL,
		VLogsURL:    vlogsURL,
		VMetricsURL: vmetricsURL,
	}, db, store, raftNode, hosts, broker, seq, log)

	log.Info("floatlab-control starting",
		zap.String("listen", listenAddr),
		zap.String("raft_id", raftNodeID),
		zap.String("rqlite", rqliteURL),
		zap.String("vlogs", vlogsURL),
		zap.String("vmetrics", vmetricsURL),
	)

	return srv.Run(ctx)
}
