package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"
)

const ExecTimeout = 60 * time.Second
const ExecOutputLimit = 1 << 20

// Exec runs argv directly, without a TTY or stdin. The deadline bounds capture;
// Docker has no exec-kill endpoint, so disconnecting does not stop the command.
func (c *Client) Exec(ctx context.Context, stackID, containerID string, command []string) (ipc.ExecResult, error) {
	if len(command) == 0 || command[0] == "" {
		return ipc.ExecResult{}, fmt.Errorf("command is required")
	}
	ctx, cancel := context.WithTimeout(ctx, ExecTimeout)
	defer cancel()
	inspected, err := c.dc.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return ipc.ExecResult{}, err
	}
	if inspected.Container.Config == nil || inspected.Container.Config.Labels[LabelComposeProject] != stackID {
		return ipc.ExecResult{}, fmt.Errorf("container does not belong to stack")
	}
	created, err := c.dc.ExecCreate(ctx, containerID, dockerclient.ExecCreateOptions{Cmd: command, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return ipc.ExecResult{}, err
	}
	attached, err := c.dc.ExecAttach(ctx, created.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		return ipc.ExecResult{}, err
	}
	defer attached.Close()
	done := make(chan error, 1)
	var stdout, stderr bytes.Buffer
	remaining := ExecOutputLimit
	go func() {
		_, err := stdcopy.StdCopy(&execWriter{&stdout, &remaining}, &execWriter{&stderr, &remaining}, attached.Reader)
		done <- err
	}()
	select {
	case <-ctx.Done():
		attached.Close()
		<-done
		return ipc.ExecResult{}, fmt.Errorf("exec capture cancelled or timed out: %w", ctx.Err())
	case err := <-done:
		if err != nil {
			return ipc.ExecResult{}, fmt.Errorf("exec output: %w", err)
		}
	}
	for {
		result, err := c.dc.ExecInspect(ctx, created.ID, dockerclient.ExecInspectOptions{})
		if err != nil {
			return ipc.ExecResult{}, err
		}
		if !result.Running {
			return ipc.ExecResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: result.ExitCode}, nil
		}
		select {
		case <-ctx.Done():
			return ipc.ExecResult{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var errExecOutputLimit = errors.New("exec output exceeds 1 MiB limit")

type execWriter struct {
	buffer    *bytes.Buffer
	remaining *int
}

func (w *execWriter) Write(data []byte) (int, error) {
	if len(data) > *w.remaining {
		return 0, errExecOutputLimit
	}
	n, err := w.buffer.Write(data)
	*w.remaining -= n
	return n, err
}

var _ io.Writer = (*execWriter)(nil)
