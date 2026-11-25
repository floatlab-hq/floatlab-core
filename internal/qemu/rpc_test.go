package qemu

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func fakeAgent(t *testing.T, replies ...string) *Client {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	go func() {
		defer server.Close()
		decoder := json.NewDecoder(server)
		for _, reply := range replies {
			var request struct{ Execute string }
			if err := decoder.Decode(&request); err != nil {
				return
			}
			if _, err := io.WriteString(server, reply); err != nil {
				return
			}
		}
	}()
	return &Client{Conn: client, encoder: json.NewEncoder(client), decoder: json.NewDecoder(client)}
}

func TestGuestExecDecodesOutputAndWaitsForExit(t *testing.T) {
	c := fakeAgent(t,
		`{"return":{"pid":123}}`+"\n",
		`{"return":{"exited":false}}`+"\n",
		`{"return":{"exited":true,"exitcode":0,"out-data":"aGVsbG8K"}}`+"\n")
	output, err := c.GuestExec(t.Context(), "/bin/echo", []string{"hello"}, time.Millisecond)
	if err != nil || output != "hello\n" {
		t.Fatalf("output = %q, error = %v", output, err)
	}
}

func TestGuestExecReportsGuestExitStatus(t *testing.T) {
	c := fakeAgent(t, "{\"return\":{\"pid\":123}}\n",
		"{\"return\":{\"exited\":true,\"exitcode\":255,\"err-data\":\"ZGVuaWVk\"}}\n")
	_, err := c.GuestExec(t.Context(), "/bin/ssh", nil, time.Millisecond)
	var exit *GuestExitError
	if !errors.As(err, &exit) || exit.Code != 255 || exit.Signal != 0 || exit.Output != "denied" {
		t.Fatalf("guest exit = %v", err)
	}
}

func TestMonitorIgnoresGreetingAndEvents(t *testing.T) {
	c := fakeAgent(t, `{"QMP":{}}
{"event":"RESET"}
{"return":{}}
`)
	if err := c.Request("qmp_capabilities", nil, nil); err != nil {
		t.Fatal(err)
	}
	err := c.Request("screendump", nil, nil)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("request after disconnect = %v", err)
	}
}
