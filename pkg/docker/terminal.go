package docker

import (
	"context"
	"fmt"
	"io"
	"sync"

	dockerclient "github.com/moby/moby/client"
)

type Terminal struct {
	ID       string
	response dockerclient.HijackedResponse
	client   *Client
	mu       sync.Mutex
}

func (c *Client) OpenTerminal(ctx context.Context, stackID, containerID string, command []string, rows, cols uint) (*Terminal, error) {
	result, err := c.dc.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	if result.Container.Config == nil || result.Container.Config.Labels[LabelComposeProject] != stackID {
		return nil, fmt.Errorf("container does not belong to stack")
	}
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}
	size := dockerclient.ConsoleSize{Height: rows, Width: cols}
	created, err := c.dc.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{
		Cmd: command, TTY: true, AttachStdin: true, AttachStdout: true, AttachStderr: true, ConsoleSize: size,
	})
	if err != nil {
		return nil, err
	}
	attached, err := c.dc.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{TTY: true, ConsoleSize: size})
	if err != nil {
		return nil, err
	}
	return &Terminal{ID: created.ID, response: attached.HijackedResponse, client: c}, nil
}

func (t *Terminal) Read(buffer []byte) (int, error) { return t.response.Reader.Read(buffer) }
func (t *Terminal) Write(data []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.response.Conn.Write(data)
}
func (t *Terminal) Resize(ctx context.Context, rows, cols uint) error {
	_, err := t.client.dc.ExecResize(ctx, t.ID, dockerclient.ExecResizeOptions{Height: rows, Width: cols})
	return err
}
func (t *Terminal) Close() error { t.response.Close(); return nil }

var _ io.ReadWriteCloser = (*Terminal)(nil)
