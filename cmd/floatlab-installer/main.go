package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/spf13/cobra"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := &cobra.Command{Use: "floatlab-installer", Short: "Install the FloatLab immutable appliance", PersistentPreRunE: func(*cobra.Command, []string) error {
		if os.Geteuid() != 0 {
			return fmt.Errorf("installer requires root")
		}
		return nil
	}, SilenceUsage: true}
	root.AddCommand(&cobra.Command{Use: "console", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return (&installer.Console{In: bufio.NewReader(os.Stdin), Out: os.Stdout, FD: int(os.Stdin.Fd())}).Run(cmd.Context())
	}}, &cobra.Command{Use: "worker", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return installer.Worker(cmd.Context()) }})
	if err := root.ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}
