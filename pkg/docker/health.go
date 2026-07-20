package docker

import (
	"context"

	dockerclient "github.com/moby/moby/client"
)

// ContainerSummary is a minimal, orchestrator-facing view of a running container.
type ContainerSummary struct {
	ID       string
	Name     string
	State    string // running, exited, dead, ...
	Health   string // healthy, unhealthy, starting, none
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
		if len(id) > 12 {
			id = id[:12]
		}
		health, exitCode := containerDetails(ctx, c, ct.ID, string(ct.State))
		out = append(out, ContainerSummary{
			ID:       id,
			Name:     firstContainerName(ct.Names),
			State:    string(ct.State),
			Health:   health,
			Image:    ct.Image,
			StackID:  stackID,
			Service:  ct.Labels["com.docker.compose.service"],
			ExitCode: exitCode,
		})
	}
	return out, nil
}

func containerDetails(ctx context.Context, c *Client, containerID, state string) (string, int) {
	result, err := c.dc.ContainerInspect(ctx, containerID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		return "none", 0
	}
	if result.Container.State == nil {
		return "none", 0
	}
	exitCode := result.Container.State.ExitCode
	if state != "running" || result.Container.State.Health == nil {
		return "none", exitCode
	}
	return string(result.Container.State.Health.Status), exitCode
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
