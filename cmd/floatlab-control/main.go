package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/control"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
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

	// Config tables.
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

	// HTTP control server.
	srv := control.NewServer(&control.Config{
		ListenAddr: listenAddr,
		RQLiteURL:  rqliteURL,
	}, store, raftNode, hosts, log)

	log.Info("floatlab-control starting",
		zap.String("listen", listenAddr),
		zap.String("raft_id", raftNodeID),
		zap.String("rqlite", rqliteURL),
	)

	return srv.Run(ctx)
}
