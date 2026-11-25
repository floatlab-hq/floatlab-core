// Command installer-vm-control operates the local installer preview.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
	"github.com/floatlab/floatlab-core/internal/qemu"
)

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: installer-vm-control {refresh|screenshot|serve}")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	var output, web string
	switch args[0] {
	case "refresh":
	case "screenshot":
		flags.StringVar(&output, "output", "/workspace/.local/installer/vm/screen.ppm", "QMP screenshot path")
	case "serve":
		flags.StringVar(&web, "web", "", "noVNC web directory")
	default:
		return fmt.Errorf("unknown action %s", args[0])
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if args[0] == "serve" {
		if web == "" {
			return errors.New("serve requires --web")
		}
		return serve(web)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if args[0] == "refresh" {
		guest, err := qemu.Dial(ctx, "/tmp/floatlab-preview-guest.sock")
		if err != nil {
			return err
		}
		defer guest.Close()
		_, err = guest.GuestExec(ctx, "/run/current-system/sw/bin/systemctl",
			[]string{"restart", "floatlab-installer-display.service"}, 100*time.Millisecond)
		return err
	}
	monitor, err := qemu.Dial(ctx, "/tmp/floatlab-preview-qmp.sock")
	if err != nil {
		return err
	}
	defer monitor.Close()
	if err := monitor.Request("qmp_capabilities", nil, nil); err != nil {
		return err
	}
	return monitor.Request("screendump", map[string]string{"filename": output}, nil)
}

func serve(web string) error {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(web)))
	mux.HandleFunc("/websockify", func(w http.ResponseWriter, r *http.Request) {
		vnc(w, r, "127.0.0.1:5900")
	})
	server := &http.Server{Addr: ":6080", Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return server.ListenAndServe()
}

func vnc(w http.ResponseWriter, r *http.Request, address string) {
	target, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(r.Context(), "tcp", address)
	if err != nil {
		http.Error(w, "preview is starting", http.StatusServiceUnavailable)
		return
	}
	defer target.Close()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"binary"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	client := websocket.NetConn(ctx, conn, websocket.MessageBinary)
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(target, client); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, target); done <- struct{}{} }()
	<-done
}

func main() {
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}
