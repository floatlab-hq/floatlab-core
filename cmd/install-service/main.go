package main

import (
	"context"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/installer"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "install-service requires root")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := (&installer.Service{}).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
