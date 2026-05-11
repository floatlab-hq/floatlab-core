package docker

import (
	"context"
	"encoding/json"

	dockerevents "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"go.uber.org/zap"
)

// LabelStackID is the Docker container label used to associate a container with a FloatLab stack.
const LabelStackID = "com.floatlab.stack_id"

// StreamEvents subscribes to Docker container events and emits typed IPC events to out.
// It exits when ctx is cancelled. Call in a goroutine.
func (c *Client) StreamEvents(ctx context.Context, out chan<- ipc.Event, log *zap.Logger) {
	f := filters.NewArgs()
	f.Add("type", "container")

	msgs, errs := c.dc.Events(ctx, dockerevents.ListOptions{Filters: f})
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-errs:
			if !ok || ctx.Err() != nil {
				return
			}
			log.Warn("docker events stream error", zap.Error(err))
			return
		case msg, ok := <-msgs:
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
					zap.String("action", msg.Action))
			}
		}
	}
}

func containerEventToIPC(msg dockerevents.Message) *ipc.Event {
	stackID := msg.Actor.Attributes[LabelStackID]

	switch msg.Action {
	case "start", "stop", "die", "kill", "restart", "oom":
		payload, _ := json.Marshal(ipc.ContainerStateEvent{
			ContainerID: msg.Actor.ID,
			StackID:     stackID,
			Status:      msg.Action,
		})
		return &ipc.Event{Name: "container.state", Payload: payload}

	case "health_status":
		health := msg.Actor.Attributes["health_status"]
		payload, _ := json.Marshal(ipc.ContainerHealthEvent{
			ContainerID: msg.Actor.ID,
			StackID:     stackID,
			Health:      health,
		})
		return &ipc.Event{Name: "container.health", Payload: payload}
	}
	return nil
}
