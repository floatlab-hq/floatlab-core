package docker

import (
	"context"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// ContainerSummary is a minimal, orchestrator-facing view of a running container.
type ContainerSummary struct {
	ID      string
	Name    string
	State   string // running, exited, dead, ...
	Health  string // healthy, unhealthy, starting, none
	Image   string
	StackID string // com.floatlab.stack_id label
	Service string // com.docker.compose.service label
}

// ListByStack returns all containers (running or stopped) labeled with stackID.
func (c *Client) ListByStack(ctx context.Context, stackID string) ([]ContainerSummary, error) {
	f := filters.NewArgs()
	f.Add("label", LabelStackID+"="+stackID)

	list, err := c.dc.ContainerList(ctx, container.ListOptions{All: true, Filters: f})
	if err != nil {
		return nil, err
	}

	out := make([]ContainerSummary, 0, len(list))
	for _, ct := range list {
		id := ct.ID
		if len(id) > 12 {
			id = id[:12]
		}
		out = append(out, ContainerSummary{
			ID:      id,
			Name:    firstContainerName(ct.Names),
			State:   ct.State,
			Health:  containerHealth(ctx, c, ct.ID, ct.State),
			Image:   ct.Image,
			StackID: ct.Labels[LabelStackID],
			Service: ct.Labels["com.docker.compose.service"],
		})
	}
	return out, nil
}

func containerHealth(ctx context.Context, c *Client, containerID, state string) string {
	if state != "running" {
		return "none"
	}
	info, err := c.dc.ContainerInspect(ctx, containerID)
	if err != nil {
		return "none"
	}
	if info.State == nil || info.State.Health == nil {
		return "none"
	}
	return string(info.State.Health.Status)
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
