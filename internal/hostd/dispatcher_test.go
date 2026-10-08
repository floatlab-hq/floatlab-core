package hostd

import (
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestSnapshotRollbackCommandAndBoundary(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ROLLBACK_ARGS\"\nexit \"$ROLLBACK_EXIT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("ROLLBACK_ARGS", argsPath)
	t.Setenv("ROLLBACK_EXIT", "0")
	d := &Dispatcher{}
	for _, destroy := range []bool{false, true} {
		raw, _ := json.Marshal(ipc.SnapshotRollbackPayload{Dataset: "floatlab/stacks/test", Name: "before", DestroyNewer: destroy})
		if _, err := d.snapshotRollback(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		args, err := os.ReadFile(argsPath)
		want := "rollback\nfloatlab/stacks/test@before\n"
		if destroy {
			want = "rollback\n-r\nfloatlab/stacks/test@before\n"
		}
		if err != nil || string(args) != want {
			t.Fatalf("args %q: %v", args, err)
		}
	}
	if err := os.Remove(argsPath); err != nil {
		t.Fatal(err)
	}
	for _, target := range []ipc.SnapshotRollbackPayload{
		{Dataset: "floatlab/../etc", Name: "before"},
		{Dataset: "floatlab/stacks/test", Name: "before;touch /tmp/no"},
		{Dataset: "floatlab/stacks/test", Name: "before@extra"},
	} {
		raw, _ := json.Marshal(target)
		if _, err := d.snapshotRollback(context.Background(), raw); err == nil {
			t.Fatalf("accepted %+v", target)
		}
	}
	if _, err := os.Stat(argsPath); !os.IsNotExist(err) {
		t.Fatal("invalid targets reached zfs")
	}
	t.Setenv("ROLLBACK_EXIT", "1")
	raw := json.RawMessage(`{"dataset":"floatlab/stacks/test","name":"before"}`)
	result, err := d.snapshotRollback(context.Background(), raw)
	if err == nil || result.(map[string]bool)["restored"] || !strings.Contains(err.Error(), "exit status") {
		t.Fatalf("failed rollback: %v %v", result, err)
	}
}
