package docker

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	dockerevents "github.com/moby/moby/api/types/events"
	dockerclient "github.com/moby/moby/client"
	"go.uber.org/zap"
)

// LabelStackID is the Docker container label used to associate a container with a FloatLab stack.
const LabelStackID = "com.floatlab.stack_id"

const LabelComposeProject = "com.docker.compose.project"

// StreamEvents subscribes to Docker container events and emits typed IPC events to out.
// It exits when ctx is cancelled. Call in a goroutine.
func (c *Client) StreamEvents(ctx context.Context, out chan<- ipc.Event, log *zap.Logger) {
	f := make(dockerclient.Filters).Add("type", "container")

	result := c.dc.Events(ctx, dockerclient.EventsListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-result.Err:
			if !ok || ctx.Err() != nil {
				return
			}
			log.Warn("docker events stream error", zap.Error(err))
			return
		case msg, ok := <-result.Messages:
			if !ok {
				return
			}
			ev := containerEventToIPC(msg)
			if ev == nil {
				continue
			}
			select {
			case out <- *ev:
			default:
				log.Warn("docker events: out channel full, dropping event",
					zap.String("action", string(msg.Action)))
			}
		}
	}
}

func containerEventToIPC(msg dockerevents.Message) *ipc.Event {
	stackID := msg.Actor.Attributes[LabelStackID]
	if stackID == "" {
		stackID = msg.Actor.Attributes[LabelComposeProject]
	}
	if stackID == "" {
		return nil
	}
	action := string(msg.Action)
	if health, ok := strings.CutPrefix(action, "health_status: "); ok {
		payload, _ := json.Marshal(ipc.ContainerHealthEvent{
			ContainerID: msg.Actor.ID,
			StackID:     stackID,
			Health:      health,
		})
		return &ipc.Event{Name: "container.health", Payload: payload}
	}

	switch action {
	case "start", "stop", "die", "kill", "restart", "oom":
		payload, _ := json.Marshal(ipc.ContainerStateEvent{
			ContainerID: msg.Actor.ID,
			StackID:     stackID,
			Status:      action,
			Service:     msg.Actor.Attributes["com.docker.compose.service"],
			Image:       msg.Actor.Attributes["image"],
			ExitStatus:  msg.Actor.Attributes["exitCode"],
		})
		return &ipc.Event{Name: "container.state", Payload: payload}

	}
	return nil
}
