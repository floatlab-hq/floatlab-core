// Package qemu talks to QEMU's monitor and guest agent over local sockets.
package qemu

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

type Client struct {
	net.Conn
	encoder *json.Encoder
	decoder *json.Decoder
}

type GuestExitError struct {
	Code, Signal int
	Output       string
}

func (e *GuestExitError) Error() string {
	return fmt.Sprintf("guest command (exit %d, signal %d): %s", e.Code, e.Signal, e.Output)
}

func Dial(ctx context.Context, socket string) (*Client, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	return &Client{Conn: conn, encoder: json.NewEncoder(conn), decoder: json.NewDecoder(conn)}, nil
}

func (c *Client) Request(command string, arguments any, result any) error {
	if err := c.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	request := struct {
		Execute   string `json:"execute"`
		Arguments any    `json:"arguments,omitempty"`
	}{command, arguments}
	if err := c.encoder.Encode(request); err != nil {
		return err
	}
	for {
		var response struct {
			Return json.RawMessage `json:"return"`
			Error  json.RawMessage `json:"error"`
		}
		if err := c.decoder.Decode(&response); err != nil {
			return err
		}
		// The QMP greeting and asynchronous monitor events have neither field.
		if len(response.Error) != 0 {
			return fmt.Errorf("%s: %s", command, response.Error)
		}
		if len(response.Return) != 0 {
			if result == nil {
				return nil
			}
			return json.Unmarshal(response.Return, result)
		}
	}
}

func (c *Client) GuestExec(ctx context.Context, path string, args []string, interval time.Duration) (string, error) {
	var process struct{ PID int }
	if err := c.Request("guest-exec", map[string]any{"path": path, "arg": args, "capture-output": true}, &process); err != nil {
		return "", err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		var status struct {
			Exited   bool
			ExitCode int
			Signal   int
			Out      []byte `json:"out-data"`
			Err      []byte `json:"err-data"`
		}
		if err := c.Request("guest-exec-status", map[string]int{"pid": process.PID}, &status); err != nil {
			return "", err
		}
		if status.Exited {
			if status.ExitCode != 0 || status.Signal != 0 {
				return "", &GuestExitError{Code: status.ExitCode, Signal: status.Signal, Output: string(status.Err) + string(status.Out)}
			}
			return string(status.Out), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
