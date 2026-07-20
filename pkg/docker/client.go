package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	composeapi "github.com/docker/compose/v5/pkg/api"
	composesdk "github.com/docker/compose/v5/pkg/compose"
	dockerclient "github.com/moby/moby/client"
)

// Client wraps the Docker daemon client for use by hostd.
type Client struct {
	dc      *dockerclient.Client
	compose composeapi.Compose
}

// New creates a Docker client, connecting to DOCKER_HOST or the default UNIX socket.
func New() (*Client, error) {
	dc, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}
	dockerCLI, err := command.NewDockerCli()
	if err != nil {
		dc.Close()
		return nil, fmt.Errorf("docker: compose CLI adapter: %w", err)
	}
	if err := dockerCLI.Initialize(&flags.ClientOptions{}); err != nil {
		dc.Close()
		return nil, fmt.Errorf("docker: compose initialize: %w", err)
	}
	service, err := composesdk.NewComposeService(dockerCLI, composesdk.WithStreams(io.Discard, io.Discard, nil), composesdk.WithPrompt(composesdk.AlwaysOkPrompt()))
	if err != nil {
		dc.Close()
		return nil, fmt.Errorf("docker: compose service: %w", err)
	}
	return &Client{dc: dc, compose: service}, nil
}

// Ping verifies connectivity to the Docker daemon.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.dc.Ping(ctx, dockerclient.PingOptions{})
	return err
}

func (c *Client) Close() error { return c.dc.Close() }

func (c *Client) ComposeUp(ctx context.Context, projectName, path string) error {
	project, err := c.compose.LoadProject(ctx, composeapi.ProjectLoadOptions{ConfigPaths: []string{path}, ProjectName: projectName})
	if err != nil {
		return err
	}
	return c.compose.Up(ctx, project, composeapi.UpOptions{Create: composeapi.CreateOptions{Build: &composeapi.BuildOptions{Pull: true}, RemoveOrphans: true}, Start: composeapi.StartOptions{}})
}

func (c *Client) ComposeDown(ctx context.Context, projectName, path string, volumes bool) error {
	project, err := c.compose.LoadProject(ctx, composeapi.ProjectLoadOptions{ConfigPaths: []string{path}, ProjectName: projectName})
	if err != nil {
		return err
	}
	return c.compose.Down(ctx, projectName, composeapi.DownOptions{Project: project, RemoveOrphans: true, Volumes: volumes})
}

func (c *Client) ComposePull(ctx context.Context, projectName, path string, services []string) error {
	project, err := c.compose.LoadProject(ctx, composeapi.ProjectLoadOptions{ConfigPaths: []string{path}, ProjectName: projectName})
	if err != nil {
		return err
	}
	if len(services) > 0 {
		selected, err := project.WithSelectedServices(services)
		if err != nil {
			return err
		}
		project = selected
	}
	return c.compose.Pull(ctx, project, composeapi.PullOptions{IgnoreBuildable: true})
}
