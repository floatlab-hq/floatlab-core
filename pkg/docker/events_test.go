package docker

import (
	"encoding/json"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	dockerevents "github.com/moby/moby/api/types/events"
)

func TestContainerEventUsesComposeProject(t *testing.T) {
	event := containerEventToIPC(dockerevents.Message{
		Action: "health_status: healthy",
		Actor: dockerevents.Actor{ID: "container-1", Attributes: map[string]string{
			LabelComposeProject: "stack-123",
		}},
	})
	if event == nil || event.Name != "container.health" {
		t.Fatalf("unexpected event: %#v", event)
	}
	var payload ipc.ContainerHealthEvent
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.StackID != "stack-123" || payload.Health != "healthy" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if containerEventToIPC(dockerevents.Message{Action: "die"}) != nil {
		t.Fatal("unmanaged container event was not ignored")
	}
}
