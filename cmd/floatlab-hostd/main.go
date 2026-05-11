package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/hostd"
)

func main() {
	root := &cobra.Command{
		Use:   "floatlab-hostd",
		Short: "FloatLab host daemon — manages filesystem and containers on behalf of the control plane",
		RunE:  run,
	}
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
