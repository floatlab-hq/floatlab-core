package docker

import (
	"context"

	dockerclient "github.com/moby/moby/client"
)

// StartContainer starts one existing container.
func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	_, err := c.dc.ContainerStart(ctx, containerID, dockerclient.ContainerStartOptions{})
	return err
}

// StopContainer stops one existing container.
func (c *Client) StopContainer(ctx context.Context, containerID string) error {
	_, err := c.dc.ContainerStop(ctx, containerID, dockerclient.ContainerStopOptions{})
	return err
}

// ContainerSummary is a minimal, orchestrator-facing view of a running container.
type ContainerSummary struct {
	ID       string
	Name     string
	State    string // running, exited, dead, ...
	Health   string // healthy, unhealthy, starting, none, unknown
	Image    string
	StackID  string // com.floatlab.stack_id label
	Service  string // com.docker.compose.service label
	ExitCode int
}

// ListByStack returns all containers (running or stopped) in the Compose project.
func (c *Client) ListByStack(ctx context.Context, stackID string) ([]ContainerSummary, error) {
	f := make(dockerclient.Filters).Add("label", LabelComposeProject+"="+stackID)

	result, err := c.dc.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}

	out := make([]ContainerSummary, 0, len(result.Items))
	for _, ct := range result.Items {
		id := ct.ID
		state, health, exitCode := containerDetails(ctx, c, ct.ID, string(ct.State))
		out = append(out, ContainerSummary{
			ID:       id,
			Name:     firstContainerName(ct.Names),
			State:    state,
			Health:   health,
			Image:    ct.Image,
			StackID:  stackID,
			Service:  ct.Labels["com.docker.compose.service"],
			ExitCode: exitCode,
		})
	}
	return out, nil
}

func containerDetails(ctx context.Context, c *Client, containerID, state string) (string, string, int) {
	result, err := c.dc.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil || result.Container.State == nil {
		return state, "unknown", 0
	}
	reported := result.Container.State
	if reported.Status != "" {
		state = string(reported.Status)
	}
	if reported.Restarting {
		state = "restarting"
	}
	health := "none"
	if state == "running" && reported.Health != nil {
		health = string(reported.Health.Status)
	}
	return state, health, reported.ExitCode
}

func firstContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	n := names[0]
	if len(n) > 0 && n[0] == '/' {
		return n[1:]
	}
	return n
}
