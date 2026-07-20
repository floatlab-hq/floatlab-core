package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestContainerAPI(t *testing.T) {
	if os.Getenv("FLOATLAB_VM_INTEGRATION") != "1" {
		t.Skip("run with FLOATLAB_VM_INTEGRATION=1 go test -count=1 ./integration")
	}

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "scripts/test-container-api-vm.sh"))
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = root, os.Environ(), os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
