package hostd

import (
	"reflect"
	"testing"
)

func TestComposeArgs(t *testing.T) {
	args, path, err := composeArgs("stack-123", "floatlab/stacks/demo", "up", "-d")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"compose", "-p", "stack-123", "-f", "/floatlab/stacks/demo/docker-compose.yml", "up", "-d"}
	if !reflect.DeepEqual(args, want) || path != want[4] {
		t.Fatalf("composeArgs() = %v, %q", args, path)
	}
	if _, _, err := composeArgs("stack-123", "floatlab/../etc", "down"); err == nil {
		t.Fatal("composeArgs accepted traversal")
	}
}
