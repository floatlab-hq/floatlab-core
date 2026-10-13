package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/installer"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	scenario := flag.String("scenario", "success", "success, delayed-config, rejected-config, failure, cancellation")
	delay := flag.Duration("step-delay", 250*time.Millisecond, "time between simulated progress events")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := (&installer.Service{Mock: true, Scenario: *scenario, StepDelay: *delay}).Serve(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
