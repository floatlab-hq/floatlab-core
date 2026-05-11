package docker

import (
	"context"
	"fmt"

	dockerclient "github.com/docker/docker/client"
)

// Client wraps the Docker daemon client for use by hostd.
type Client struct {
	dc *dockerclient.Client
}

// New creates a Docker client, connecting to DOCKER_HOST or the default UNIX socket.
func New() (*Client, error) {
	dc, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	return &Client{dc: dc}, nil
}

// Ping verifies connectivity to the Docker daemon.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.dc.Ping(ctx)
	return err
}

func (c *Client) Close() error { return c.dc.Close() }
