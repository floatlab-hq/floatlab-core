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

func TestDatasetCreateMakesComposeSourceWritable(t *testing.T) {
	for _, state := range []string{"missing", "unmounted", "mounted", "wrong-mountpoint"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			mountpoint := filepath.Join(dir, "stack")
			dataset := strings.TrimPrefix(mountpoint, "/")
			// Model a pool with mountpoint=none: only an explicit mountpoint can mount it.
			script := `#!/bin/sh
case "$1" in
list) [ "$DATASET_STATE" != missing ] ;;
get)
  case "$5" in
  mountpoint)
    if [ "$DATASET_STATE" = wrong-mountpoint ]; then echo none; else echo "$DATASET_MOUNT"; fi ;;
  mounted)
    if [ "$DATASET_STATE" = mounted ]; then echo yes; else echo no; fi ;;
  esac ;;
create)
  for arg in "$@"; do
    if [ "$arg" = "mountpoint=$DATASET_MOUNT" ]; then mkdir "$DATASET_MOUNT"; fi
  done ;;
set) [ "$2" = "mountpoint=$DATASET_MOUNT" ] && touch "$DATASET_MOUNT.set" ;;
mount)
  if [ "$DATASET_STATE" = wrong-mountpoint ]; then test -f "$DATASET_MOUNT.set" || exit 1; fi
  mkdir "$DATASET_MOUNT" ;;
*) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("DATASET_STATE", state)
			t.Setenv("DATASET_MOUNT", mountpoint)
			if state == "mounted" {
				if err := os.Mkdir(mountpoint, 0755); err != nil {
					t.Fatal(err)
				}
			}
			d := &Dispatcher{}
			raw, _ := json.Marshal(ipc.DatasetCreatePayload{Dataset: dataset})
			if _, err := d.datasetCreate(context.Background(), raw); err != nil {
				t.Fatal(err)
			}
			const compose = "services: {}\n"
			raw, _ = json.Marshal(ipc.ComposeSourcePayload{StackID: "test", DatasetPath: dataset, ComposeFile: compose})
			if _, err := d.composeSourceWrite(context.Background(), raw); err != nil {
				t.Fatalf("write compose source after dataset creation: %v", err)
			}
			content, err := os.ReadFile(filepath.Join(mountpoint, "docker-compose.yml"))
			if err != nil || string(content) != compose {
				t.Fatalf("compose source = %q, %v", content, err)
			}
		})
	}
}

func TestDatasetCreatePropagatesMountErrors(t *testing.T) {
	for _, command := range []string{"create", "get:mountpoint", "set", "get:mounted", "mount"} {
		t.Run(command, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/bin/sh
operation="$1"
if [ "$operation" = get ]; then operation="get:$5"; fi
if [ "$operation" = "$FAIL_COMMAND" ]; then exit 1; fi
if [ "$operation" = list ] && [ "$FAIL_COMMAND" = create ]; then exit 1; fi
case "$operation" in
get:mountpoint) echo none ;;
get:mounted) echo no ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			t.Setenv("FAIL_COMMAND", command)
			d := &Dispatcher{}
			if _, err := d.datasetCreate(context.Background(), json.RawMessage(`{"dataset":"floatlab/test"}`)); err == nil {
				t.Fatalf("dataset creation succeeded despite failed %s", command)
			}
		})
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
