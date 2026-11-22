package docker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestRestartAfterBootPreservesDockerPolicy(t *testing.T) {
	root := t.TempDir()
	projectContainer := container.InspectResponse{
		ID: "container", State: &container.State{StartedAt: "2025-06-01T00:00:00Z"},
		HostConfig: &container.HostConfig{},
	}
	dir := filepath.Join(root, "containers", projectContainer.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		policy   container.RestartPolicyMode
		metadata string
		want     bool
	}{
		{"disabled", container.RestartPolicyDisabled, "", false},
		{"always", container.RestartPolicyAlways, "", true},
		{"unless stopped", container.RestartPolicyUnlessStopped, `{"HasBeenManuallyStopped":false}`, true},
		{"manually stopped", container.RestartPolicyUnlessStopped, `{"HasBeenManuallyStopped":true}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "config.v2.json"), []byte(test.metadata), 0600); err != nil {
				t.Fatal(err)
			}
			projectContainer.HostConfig.RestartPolicy.Name = test.policy
			if restart, err := restartAfterBoot(root, projectContainer); err != nil || restart != test.want {
				t.Fatalf("restart=%t, want %t: %v", restart, test.want, err)
			}
		})
	}
}
