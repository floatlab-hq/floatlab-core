package hostd

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/ipc"
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

func TestComposeSourceWritePublishesCompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	const old = "name: old\nservices: {}\n"
	const next = "name: next\nservices: {}\n"
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	payload, err := json.Marshal(ipc.ComposeSourcePayload{StackID: "test", DatasetPath: strings.TrimPrefix(dir, "/"), ComposeFile: next})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&Dispatcher{}).composeSourceWrite(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	if prior, err := io.ReadAll(reader); err != nil || string(prior) != old {
		t.Fatalf("existing reader got %q, %v", prior, err)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != next {
		t.Fatalf("new reader got %q, %v", current, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("compose mode is %o, want 0600", info.Mode().Perm())
	}
	state, err := sql.Open("sqlite", filepath.Join(dir, compose.ProjectStateDir, stackStateFile))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	// Model an interruption after the file rename and before the tracking commit.
	if _, err := state.Exec(`DELETE FROM compose_source`); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Dispatcher{}).composeSourceWrite(context.Background(), payload); err != nil {
		t.Fatal(err)
	}
	var stackID, digest string
	if err := state.QueryRow(`SELECT stack_id,sha256 FROM compose_source`).Scan(&stackID, &digest); err != nil {
		t.Fatal(err)
	}
	if stackID != "test" || digest != fmt.Sprintf("%x", sha256.Sum256([]byte(next))) {
		t.Fatalf("recorded unexpected Compose publication: %s %s", stackID, digest)
	}
	if retried, err := os.Stat(path); err != nil || !os.SameFile(info, retried) {
		t.Fatalf("retry rewrote an already published file: %v", err)
	}
}

func TestComposeSourceReadUsesRequestedSnapshot(t *testing.T) {
	dir := t.TempDir()
	snapshot := filepath.Join(dir, ".zfs", "snapshot", "saved")
	if err := os.MkdirAll(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	const saved = "services: {web: {image: nginx:1}}\n"
	if err := os.WriteFile(filepath.Join(snapshot, "docker-compose.yml"), []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	dataset := strings.TrimPrefix(dir, "/")
	payload := func(name string) json.RawMessage {
		data, err := json.Marshal(ipc.ComposeSourceReadPayload{StackID: "test", DatasetPath: dataset, Snapshot: name})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	d := &Dispatcher{}
	result, err := d.composeSourceRead(t.Context(), payload("saved"))
	if err != nil || result != (ipc.ComposeSourceReadResult{ComposeFile: saved}) {
		t.Fatalf("snapshot Compose = %#v, %v", result, err)
	}
	if _, err := d.composeSourceRead(t.Context(), payload("../saved")); err == nil {
		t.Fatal("snapshot accepted a path outside its managed name")
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
