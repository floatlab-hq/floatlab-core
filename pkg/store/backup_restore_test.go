package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The command fixture checks the real ownership/activation sequence without needing a ZFS pool.
func restoreFixture(t *testing.T) (string, func(string, map[string]string)) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$RESTORE_TEST_STATE/commands"
action=$1; shift
last=; previous=
for arg in "$@"; do previous=$last; last=$arg; done
case "$action" in
get)
  test -d "$RESTORE_TEST_STATE/$last" || exit 1
  if test -f "$RESTORE_TEST_STATE/$last/$previous"; then cat "$RESTORE_TEST_STATE/$last/$previous"; else printf '%s\n' -; fi ;;
list)
  test -d "$RESTORE_TEST_STATE/$last" || exit 1
  case " $* " in
  *" -t snapshot "*) cat "$RESTORE_TEST_STATE/$last/snapshots" ;;
  *" -r "*) cd "$RESTORE_TEST_STATE" && find "$last" -type d ;;
  *) printf '%s\n' "$last" ;;
  esac ;;
create|clone)
  : > "$RESTORE_TEST_STATE/options"
  target=; origin=
  while test "$#" -gt 0; do
    case "$1" in
    -o) printf '%s\n' "$2" >> "$RESTORE_TEST_STATE/options"; shift 2 ;;
    -p) shift ;;
    *) origin=$target; target=$1; shift ;;
    esac
  done
  test ! -d "$RESTORE_TEST_STATE/$target" || exit 1
  mkdir -p "$RESTORE_TEST_STATE/$target"
  while IFS= read -r value; do name=${value%%=*}; printf '%s' "${value#*=}" > "$RESTORE_TEST_STATE/$target/$name"; done < "$RESTORE_TEST_STATE/options"
  if test "$action" = clone; then printf '%s' "$origin" > "$RESTORE_TEST_STATE/$target/origin"; fi ;;
rename)
  if test "$1" = -p; then shift; mkdir -p "$RESTORE_TEST_STATE/${2%/*}"; fi
  test ! -d "$RESTORE_TEST_STATE/$2" || exit 1
  mv "$RESTORE_TEST_STATE/$1" "$RESTORE_TEST_STATE/$2" &&
    find "$RESTORE_TEST_STATE/$2" -type f -name snapshots -exec sed -i "s|^$1|$2|" {} + ;;
destroy)
  test "$1" != -r || shift
  rm -r "$RESTORE_TEST_STATE/$1" ;;
mount)
  printf yes > "$RESTORE_TEST_STATE/$last/mounted" ;;
set)
  if test "${RESTORE_TEST_FAIL_MOUNT:-}" = "$last" && test "$1" = readonly=off; then exit 1; fi
  for value in "$@"; do
    test "$value" != "$last" || break
    name=${value%%=*}; printf '%s' "${value#*=}" > "$RESTORE_TEST_STATE/$last/$name"
    if test "$value" = mountpoint=none; then printf no > "$RESTORE_TEST_STATE/$last/mounted"; fi
  done ;;
*) echo "unexpected zfs action: $action" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RESTORE_TEST_STATE", dir)
	put := func(dataset string, properties map[string]string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, dataset), 0700); err != nil {
			t.Fatal(err)
		}
		for name, value := range properties {
			if err := os.WriteFile(filepath.Join(dir, dataset, name), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir, put
}

func TestLocalBackupRestoreClonesAndActivatesWithoutChangingBackup(t *testing.T) {
	_, put := restoreFixture(t)
	ctx := context.Background()
	backup := "tank/backups/source/tank/original/data"
	m := restoreMetadata{"op", "mount", "fl-000000", "123"}
	put(backup+"@"+m.Snapshot, map[string]string{"guid": m.GUID})
	stage := RestoreDataset("tank", "backup", m.OperationID, m.MountID)
	if err := prepareLocalRestore(ctx, backup, stage, "backup", m); err != nil {
		t.Fatal(err)
	}
	if err := prepareLocalRestore(ctx, backup, stage, "backup", m); err != nil {
		t.Fatalf("prepare retry: %v", err)
	}
	if value, err := run(ctx, "zfs", "get", "-H", "-o", "value", "readonly", stage); err != nil || value != "on" {
		t.Fatalf("stage was not protected: %q %v", value, err)
	}
	if err := reserveBackupRestoreLocked(ctx, "op", "tank/restored"); err != nil {
		t.Fatal(err)
	}
	if err := reserveBackupRestoreLocked(ctx, "op", "tank/restored"); err != nil {
		t.Fatalf("reservation retry: %v", err)
	}
	r := BackupRestoreActivateRequest{Dataset: stage, SourceNodeID: "backup", OperationID: "op", MountID: "mount", TargetDataset: "tank/restored/data", Mountpoint: "/tank/restored/data", ExpectedGUID: "123"}
	if err := activateBackupRestoreLocked(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := activateBackupRestoreLocked(ctx, r); err != nil {
		t.Fatalf("activation retry: %v", err)
	}
	if err := prepareLocalRestore(ctx, backup, stage, "backup", m); err != nil {
		t.Fatalf("prepare after an unacknowledged activation: %v", err)
	}
	if err := activateBackupRestoreLocked(ctx, r); err != nil {
		t.Fatalf("activation after repeated prepare: %v", err)
	}
	if _, err := run(ctx, "zfs", "list", "-H", "-o", "name", stage); err == nil {
		t.Fatal("repeated prepare left duplicate staging data")
	}
	for property, expected := range map[string]string{"readonly": "off", "mounted": "yes", "mountpoint": r.Mountpoint, "origin": backup + "@" + m.Snapshot} {
		if value, err := run(ctx, "zfs", "get", "-H", "-o", "value", property, r.TargetDataset); err != nil || value != expected {
			t.Fatalf("restored %s=%q, want %q: %v", property, value, expected, err)
		}
	}
	if guid, err := run(ctx, "zfs", "get", "-H", "-o", "value", "guid", backup+"@"+m.Snapshot); err != nil || guid != m.GUID {
		t.Fatalf("backup snapshot changed: %q %v", guid, err)
	}
}

func TestBackupRestoreRejectsCollisionsAndIncompleteData(t *testing.T) {
	dir, put := restoreFixture(t)
	ctx := context.Background()
	stage := RestoreDataset("tank", "backup", "op", "mount")
	base := map[string]string{restoreOperationProperty: "op", restoreGUIDProperty: "123", restoreSourceProperty: "backup", restoreMountProperty: "mount", "snapshots": stage + "@fl-000000\t123\n"}
	put(stage, base)
	put("tank/restored", map[string]string{restoreOperationProperty: "other"})
	r := BackupRestoreActivateRequest{Dataset: stage, SourceNodeID: "backup", OperationID: "op", MountID: "mount", TargetDataset: "tank/restored/data", Mountpoint: "/tank/restored/data", ExpectedGUID: "123"}
	if err := reserveBackupRestoreLocked(ctx, "op", "tank/restored"); err == nil {
		t.Fatal("claimed another stack")
	}
	if err := activateBackupRestoreLocked(ctx, r); err == nil {
		t.Fatal("activated into another stack")
	}
	put("tank/restored", map[string]string{restoreOperationProperty: "op"})
	put(stage, map[string]string{"receive_resume_token": "saved"})
	if err := activateBackupRestoreLocked(ctx, r); err == nil {
		t.Fatal("activated an incomplete receive")
	}
	put(stage, map[string]string{"receive_resume_token": "-", "snapshots": stage + "@fl-000000\t999\n"})
	if err := activateBackupRestoreLocked(ctx, r); err == nil {
		t.Fatal("trusted descriptive GUID without verifying received snapshot")
	}
	put(stage, map[string]string{"snapshots": base["snapshots"]})
	put(r.TargetDataset, map[string]string{restoreOperationProperty: "other"})
	if err := activateBackupRestoreLocked(ctx, r); err == nil {
		t.Fatal("overwrote an unrelated workload")
	}
	commands, err := os.ReadFile(filepath.Join(dir, "commands"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"rename ", "destroy ", "readonly=off"} {
		if strings.Contains(string(commands), forbidden) {
			t.Fatalf("unsafe mutation after failed precondition: %s", commands)
		}
	}
}

func TestBackupRestoreRejectsForeignAndLiveReceivePaths(t *testing.T) {
	m := restoreMetadata{"op", "mount", "fl-000000", "123"}
	raw, _ := json.Marshal(m)
	metadata := base64.RawURLEncoding.EncodeToString(raw)
	for _, dataset := range []string{"tank/original/data", "tank/backups/source/tank/original/data", "tank/restores/other/op/mount", "tank/restores/backup/other/mount", "tank/restores/backup/op/../mount"} {
		if err := ServeRestoreReceiver(context.Background(), "backup", "zfs-restore receive "+dataset+" "+metadata, nil, nil, nil); err == nil {
			t.Fatalf("accepted foreign restore receiver path %q", dataset)
		}
	}
	for _, target := range []string{"tank/system/data", "tank/backups/data", "tank/restores/data", "other/restored/data"} {
		r := BackupRestoreActivateRequest{Dataset: "tank/restores/backup/op/mount", SourceNodeID: "backup", OperationID: "op", MountID: "mount", ExpectedGUID: "123", TargetDataset: target, Mountpoint: "/" + target}
		if err := ActivateBackupRestore(context.Background(), r); err == nil {
			t.Fatalf("accepted unsafe activation target %q", target)
		}
	}
	if _, err := PrepareBackupRestore(context.Background(), BackupRestorePrepareRequest{SourceNodeID: "source", BackupNodeID: "backup", Dataset: "tank/original/data", Snapshot: "fl-000000", OperationID: "op", MountID: "mount", DestNodeID: "remote", DestHost: ";touch"}); err == nil {
		t.Fatal("accepted invalid remote host")
	}
}

func TestRestoreMountValidationAcceptsDottedManagedMount(t *testing.T) {
	metadata := restoreMetadata{"op", "postgres.data", "fl-000000", "123"}
	if !validRestoreMetadata(metadata) || !validRestoreStage("tank/restores/backup/op/postgres.data", "backup", "op", "postgres.data") {
		t.Fatal("valid dotted managed mount rejected")
	}
	for _, mount := range []string{".", "..", "../data", "data/child", "data;touch"} {
		if validRestoreMount(mount) {
			t.Fatalf("unsafe mount component accepted: %q", mount)
		}
	}
}

func TestRemoteBackupRestoreResumesOrRetiresSavedReceive(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "retire"}[invalid], func(t *testing.T) {
			dir := t.TempDir()
			zfs := `#!/bin/sh
printf '%s\n' "$*" >> "$RESTORE_TEST_STATE/zfs-commands"
case "$1 $2" in
"send -nvt")
  if test "$RESTORE_TEST_INVALID_TOKEN" = yes; then echo 'resume token is invalid' >&2; exit 1; fi
  printf 'toname = tank/backups/source/tank/original/data@fl-000000\ntoguid = 0x7b\n' ;;
"list -H") printf 'tank/backups/source/tank/original/data@fl-000000\t123\n' ;;
send*) printf 'snapshot stream' ;;
*) exit 1 ;;
esac
`
			ssh := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in state|receive|abort) action=$arg ;; esac
done
printf '%s\n' "$action" >> "$RESTORE_TEST_STATE/ssh-commands"
case "$action" in
state)
  if test -f "$RESTORE_TEST_STATE/complete"; then
    printf '{"Snapshots":[{"Name":"fl-000000","GUID":"123"}]}'
  elif test -f "$RESTORE_TEST_STATE/aborted"; then
    printf '{"Snapshots":[]}'
  else
    printf '{"ResumeToken":"saved","Snapshots":[]}'
  fi ;;
abort) touch "$RESTORE_TEST_STATE/aborted" ;;
receive) cat >/dev/null; touch "$RESTORE_TEST_STATE/complete" ;;
*) exit 1 ;;
esac
`
			for name, script := range map[string]string{"zfs": zfs, "ssh": ssh} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RESTORE_TEST_STATE", dir)
			if invalid {
				t.Setenv("RESTORE_TEST_INVALID_TOKEN", "yes")
			}
			r := BackupRestorePrepareRequest{SourceNodeID: "source", BackupNodeID: "backup", Dataset: "tank/original/data", Snapshot: "fl-000000", OperationID: "op", MountID: "mount", DestNodeID: "remote", DestHost: "127.0.0.1"}
			if err := normalizeRestoreDestination(&r); err != nil {
				t.Fatal(err)
			}
			if err := prepareRemoteRestore(context.Background(), r, BackupDataset(r.SourceNodeID, r.Dataset), "tank/restores/backup/op/mount", restoreMetadata{"op", "mount", "fl-000000", "123"}); err != nil {
				t.Fatal(err)
			}
			zfsCommands, _ := os.ReadFile(filepath.Join(dir, "zfs-commands"))
			sshCommands, _ := os.ReadFile(filepath.Join(dir, "ssh-commands"))
			if invalid {
				if !strings.Contains(string(sshCommands), "abort\n") || !strings.Contains(string(zfsCommands), "send tank/backups/source/tank/original/data@fl-000000\n") {
					t.Fatalf("invalid saved receive was not retired and reseeded: %s %s", zfsCommands, sshCommands)
				}
			} else if !strings.Contains(string(zfsCommands), "send -t saved\n") || strings.Contains(string(sshCommands), "abort\n") {
				t.Fatalf("saved stream did not resume: %s %s", zfsCommands, sshCommands)
			}
		})
	}
}
