// Command hostd-test-rpc invokes a hostd IPC command from a NixOS test guest.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func run() error {
	if len(os.Args) != 3 || !json.Valid([]byte(os.Args[2])) {
		return fmt.Errorf("usage: hostd-test-rpc COMMAND JSON_PAYLOAD")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := ipc.NewClient("/run/floatlab/hostd.sock")
	defer client.Close()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	payload, err := client.Execute(ctx, os.Args[1], json.RawMessage(os.Args[2]))
	if err != nil {
		return err
	}
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	fmt.Println(string(payload))
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
