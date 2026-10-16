package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/installtui"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	mock := flag.Bool("mock", false, "use install-service-mock; does not modify disks or reboot the machine")
	service := flag.String("service", "", "path to the installation service executable")
	scenario := flag.String("scenario", "success", "mock scenario")
	delay := flag.String("step-delay", "250ms", "mock progress interval")
	flag.Parse()
	var args []string
	name := "install-service"
	if *mock {
		name = "install-service-mock"
		args = []string{"--scenario", *scenario, "--step-delay", *delay}
	}
	if *service == "" {
		*service = installtui.ServicePath(name)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := installtui.Run(ctx, *service, args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
