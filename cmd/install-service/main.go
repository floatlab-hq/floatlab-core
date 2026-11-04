package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/floatlab/floatlab-core/internal/installer"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	answersPath := flag.String("answers", "", "install unattended using a TOML answers file with bcrypt password hashes")
	reboot := flag.Bool("reboot", false, "reboot after a successful unattended installation")
	flag.Parse()
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "install-service requires root")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	service := &installer.Service{}
	var err error
	if *answersPath != "" {
		var file *os.File
		file, err = os.Open(*answersPath)
		if err == nil {
			var answers installer.Answers
			answers, err = installer.ReadAnswers(file)
			file.Close()
			if err == nil {
				err = service.InstallAnswers(ctx, answers, *reboot, os.Stdout)
			}
		}
	} else if *reboot {
		err = fmt.Errorf("--reboot requires --answers")
	} else {
		err = service.Serve(ctx, os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
