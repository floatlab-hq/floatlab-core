package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

// ResumeStack restores the existing project containers Docker would start on
// boot. FloatLab calls it after confirming ownership and network availability.
func (c *Client) ResumeStack(ctx context.Context, stackID string) (bool, error) {
	list, err := c.dc.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true, Filters: make(dockerclient.Filters).Add("label", LabelComposeProject+"="+stackID),
	})
	if err != nil || len(list.Items) == 0 {
		return false, err
	}
	info, err := c.dc.Info(ctx, dockerclient.InfoOptions{})
	if err != nil {
		return true, err
	}
	var failed error
	for _, item := range list.Items {
		if item.State != container.StateExited {
			continue
		}
		result, err := c.dc.ContainerInspect(ctx, item.ID, dockerclient.ContainerInspectOptions{})
		if err == nil {
			var start bool
			start, err = restartAfterBoot(info.Info.DockerRootDir, result.Container)
			if err == nil && start {
				err = c.StartContainer(ctx, item.ID)
			}
		}
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("resume container %s: %w", item.ID, err))
		}
	}
	return true, failed
}

func restartAfterBoot(root string, c container.InspectResponse) (bool, error) {
	if c.HostConfig == nil || c.State == nil || c.State.StartedAt == "" || c.State.StartedAt == "0001-01-01T00:00:00Z" {
		return false, nil
	}
	policy := c.HostConfig.RestartPolicy
	switch {
	case policy.IsAlways():
		return true, nil
	case policy.IsOnFailure():
		return c.State.ExitCode != 0 && (policy.MaximumRetryCount == 0 || c.RestartCount < policy.MaximumRetryCount), nil
	case policy.IsUnlessStopped():
		// shortcut: Docker omits the manual-stop bit from inspect, use the API when it exposes it.
		raw, err := os.ReadFile(filepath.Join(root, "containers", c.ID, "config.v2.json"))
		if err != nil {
			return false, err
		}
		var state struct{ HasBeenManuallyStopped *bool }
		if err := json.Unmarshal(raw, &state); err != nil {
			return false, err
		}
		if state.HasBeenManuallyStopped == nil {
			return false, errors.New("Docker metadata is missing the manual-stop flag")
		}
		return !*state.HasBeenManuallyStopped, nil
	default:
		return false, nil
	}
}
