package worker

import (
	"testing"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func TestContainersReadySeparatesRunningHealthAndOneShots(t *testing.T) {
	oneShots := map[string]int{"setup": 1}
	running := ipc.ContainerInfo{Service: "app", State: "running", Health: "none"}
	setup := ipc.ContainerInfo{Service: "setup", State: "exited", Health: "none"}
	for _, test := range []struct {
		name       string
		containers []ipc.ContainerInfo
		ready      bool
		failed     bool
	}{
		{"process-only app and successful one-shot", []ipc.ContainerInfo{running, setup}, true, false},
		{"missing one-shot", []ipc.ContainerInfo{running}, false, false},
		{"unknown probe state", []ipc.ContainerInfo{{Service: "app", State: "running", Health: "unknown"}, setup}, false, false},
		{"restarting app", []ipc.ContainerInfo{{Service: "app", State: "restarting", Health: "none"}, setup}, false, false},
		{"unknown one-shot result", []ipc.ContainerInfo{running, {Service: "setup", State: "exited", Health: "unknown"}}, false, false},
		{"failed one-shot", []ipc.ContainerInfo{running, {Service: "setup", State: "exited", Health: "none", ExitCode: 2}}, false, true},
		{"unhealthy app", []ipc.ContainerInfo{{Service: "app", State: "running", Health: "unhealthy"}, setup}, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ready, err := containersReady(test.containers, oneShots)
			if ready != test.ready || (err != nil) != test.failed {
				t.Fatalf("ready = %v, err = %v", ready, err)
			}
		})
	}
}
