package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/hostd"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
)

func main() {
	root := &cobra.Command{
		Use:   "floatlab-hostd",
		Short: "FloatLab host daemon — manages filesystem and containers on behalf of the control plane",
		RunE:  run,
	}
	network := &cobra.Command{Use: "network", Short: "Initialize or restore host networking"}
	network.AddCommand(&cobra.Command{Use: "init", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return hostnetwork.New(hostnetwork.ConfigPath, hostnetwork.Linux{}).Initialize(cmd.Context())
	}})
	expired := false
	rollback := &cobra.Command{Use: "rollback CHANGE_ID", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		_, err := hostnetwork.New(hostnetwork.ConfigPath, hostnetwork.Linux{}).Rollback(args[0], expired)
		return err
	}}
	rollback.Flags().BoolVar(&expired, "expired", false, "Only restore expired unconfirmed changes")
	network.AddCommand(rollback)
	root.AddCommand(network)
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := hostd.NewServer(log)
	if err := srv.Run(ctx); err != nil {
		log.Error("hostd exited with error", zap.Error(err))
		return err
	}
	log.Info("hostd stopped cleanly")
	return nil
}
