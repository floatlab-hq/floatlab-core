package ipc

import (
	"encoding/json"
	"testing"
)

func TestDockerContainerPayloadRoundTrip(t *testing.T) {
	payload := DockerContainerPayload{StackID: "stack-1", ContainerID: "container-1"}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded DockerContainerPayload
	if err := json.Unmarshal(data, &decoded); err != nil || decoded != payload {
		t.Fatalf("DockerContainerPayload round trip = %#v, %v", decoded, err)
	}
}
