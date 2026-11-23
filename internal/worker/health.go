package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func waitForComposeReady(ctx context.Context, hosts *hostclient.Pool, nodeID, stackID, source string, duration time.Duration) error {
	oneShots, err := compose.ReadOneShotServices(source)
	if err != nil {
		return err
	}
	return WaitHealthy(ctx, hosts, nodeID, stackID, duration, oneShots)
}

// WaitHealthy requires running containers and a passing result where a probe exists.
func WaitHealthy(ctx context.Context, hosts *hostclient.Pool, nodeID, stackID string, duration time.Duration, oneShots map[string]int) error {
	waitCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		raw, err := hosts.Execute(waitCtx, nodeID, "docker.list", ipc.DockerListPayload{StackID: stackID})
		if err == nil {
			var result ipc.DockerListResult
			if json.Unmarshal(raw, &result) == nil {
				ready, err := containersReady(result.Containers, oneShots)
				if err != nil {
					return fmt.Errorf("stack %s: %w", stackID, err)
				}
				if ready {
					return nil
				}
			}
		}
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("stack %s did not become ready within %s", stackID, duration)
		case <-ticker.C:
		}
	}
}

func containersReady(containers []ipc.ContainerInfo, oneShots map[string]int) (bool, error) {
	ready := len(containers) > 0
	completed := map[string]int{}
	for _, container := range containers {
		if oneShots[container.Service] > 0 {
			if container.State != "exited" || container.Health == "unknown" {
				ready = false
				continue
			}
			if container.ExitCode != 0 {
				return false, fmt.Errorf("one-shot service %s exited with status %d", container.Service, container.ExitCode)
			}
			completed[container.Service]++
			continue
		}
		if container.Health == "unhealthy" {
			return false, fmt.Errorf("service %s has an unhealthy container", container.Service)
		}
		if container.State != "running" || (container.Health != "healthy" && container.Health != "none") {
			ready = false
		}
	}
	for service, replicas := range oneShots {
		ready = ready && completed[service] >= replicas
	}
	return ready, nil
}
