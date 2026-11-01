package integration_test

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func TestMultiNodeBackupRestore(t *testing.T) {
	source, backup, remote := newReplicationLab(t)
	sourceID, backupID, remoteID := source.nodeID(), backup.nodeID(), remote.nodeID()
	source.configurePool(1, sourceID)
	backup.configurePool(2, backupID)
	remote.configurePool(3, remoteID)
	const original = "restore-original"
	compose := strings.Replace(labWebCompose(original, sourceID, original, source.pool), `volumes: ["data:/data"]`, `volumes: ["./data:/data"]`, 1)
	compose = strings.TrimSuffix(compose, "volumes:\n  data: {}\n")
	started := source.client.startNewStack(original, compose)
	source.client.waitOperation(started.OperationID)
	source.client.waitStackState(started.StackID, "RunningPrimary")
	source.assertWeb(started.StackID, original)
	const dataset = "floatlab/restore-original/data"
	const payload = "persistent-backup-payload"
	if out, err := source.guest("printf %s " + shellQuote(payload) + " > " + shellQuote("/"+dataset+"/replicated-data")); err != nil {
		t.Fatalf("seed persistent data: %s %v", out, err)
	}
	if err := source.json("PUT", "/api/v1/storage/replication/"+started.StackID+"/targets", map[string]string{"dataset": dataset, "node_id": backupID, "schedule": "h/24 d/28"}, nil); err != nil {
		t.Fatal(err)
	}
	source.replicateAndWait(started.StackID, dataset, backupID)
	snapshot := source.recoveryPoint(dataset, backupID)
	backupDataset := store.BackupDataset(sourceID, dataset)
	assertSnapshotGUID(t, source, backup, dataset, backupDataset, snapshot)
	fingerprint := backup.restoreBackupFingerprint(backupDataset, snapshot)
	t.Logf("backed up %s@%s with matching source GUID", dataset, snapshot)

	// The original owner cannot participate in either recovery path.
	source.link(false)
	listingPath := "/api/v1/storage/backups?" + url.Values{"node_id": {backupID}, "source_node_id": {sourceID}, "dataset": {dataset}}.Encode()
	var snapshots []ipc.SnapshotInfoResult
	if err := backup.json("GET", listingPath, nil, &snapshots); err != nil {
		t.Fatalf("list backup snapshots with source offline: %v", err)
	}
	found := false
	for _, row := range snapshots {
		found = found || row.Name == snapshot
	}
	if !found {
		t.Fatalf("backup listing lacks selected point %s: %+v", snapshot, snapshots)
	}
	t.Log("backup snapshot listing works with original source offline")

	for _, target := range []struct {
		node     *labGuest
		id, name string
	}{{backup, backupID, "restore-local"}, {remote, remoteID, "restore-remote"}} {
		request := map[string]any{
			"name": target.name, "source_node_id": sourceID, "backup_node_id": backupID,
			"destination_node_id": target.id,
			"compose_file":        strings.Replace(compose, "x-fl-network-pool: "+source.pool, "x-fl-network-pool: "+target.node.pool, 1),
			"snapshots":           []map[string]string{{"dataset": dataset, "snapshot": snapshot}},
		}
		var restored struct {
			OperationID string `json:"operation_id"`
			StackID     string `json:"stack_id"`
			Status      string `json:"status"`
		}
		key := "restore-" + target.name + "-" + fmt.Sprint(time.Now().UnixNano())
		encoded := string(mustJSON(t, request))
		if err := backup.client.doJSON("POST", "/api/v1/storage/backups/restore", backup.client.token, "application/json", strings.NewReader(encoded), &restored, key); err != nil {
			t.Fatalf("restore %s: %v", target.name, err)
		}
		if restored.OperationID == "" || restored.StackID == "" || restored.StackID == started.StackID {
			t.Fatalf("restore did not create a separate stack: %+v", restored)
		}
		var repeated struct {
			OperationID string `json:"operation_id"`
			StackID     string `json:"stack_id"`
		}
		if err := backup.client.doJSON("POST", "/api/v1/storage/backups/restore", backup.client.token, "application/json", strings.NewReader(encoded), &repeated, key); err != nil || repeated.OperationID != restored.OperationID || repeated.StackID != restored.StackID {
			t.Fatalf("restore idempotency failed: original=%+v repeated=%+v error=%v", restored, repeated, err)
		}
		backup.client.waitOperation(restored.OperationID)
		target.node.client.waitStackState(restored.StackID, "RunningPrimary")
		target.node.assertWeb(restored.StackID, original)
		assertRestoredWritable(t, target.node, restored.StackID, payload)
		if err := backup.json("POST", "/api/v1/storage/backups/restore", request, nil); err == nil {
			t.Fatalf("repeat restore with a new request key accepted occupied stack name %s", target.name)
		}
		if got := backup.restoreBackupFingerprint(backupDataset, snapshot); got != fingerprint {
			t.Fatalf("restore mutated original backup: before=%q after=%q", fingerprint, got)
		}
		t.Logf("%s restored, started, read original data and wrote new data; backup unchanged", target.name)
	}

	// An unrelated dataset must never be overwritten by restore preparation.
	const collision = "floatlab/restore-collision/data"
	if out, err := remote.guest("zfs create -p -o mountpoint=none " + shellQuote(collision) + "; zfs set floatlab:acceptance-marker=untouched " + shellQuote(collision)); err != nil {
		t.Fatalf("seed restore destination collision: %s %v", out, err)
	}
	collisionRequest := map[string]any{
		"name": "restore-collision", "source_node_id": sourceID, "backup_node_id": backupID,
		"destination_node_id": remoteID,
		"compose_file":        strings.Replace(compose, "x-fl-network-pool: "+source.pool, "x-fl-network-pool: "+remote.pool, 1),
		"snapshots":           []map[string]string{{"dataset": dataset, "snapshot": snapshot}},
	}
	if err := backup.json("POST", "/api/v1/storage/backups/restore", collisionRequest, nil); err == nil {
		t.Fatal("restore accepted occupied destination dataset")
	}
	if out, err := remote.guest("zfs get -H -o value floatlab:acceptance-marker " + shellQuote(collision)); err != nil || strings.TrimSpace(out) != "untouched" {
		t.Fatalf("restore changed unrelated destination data: %q %v", out, err)
	}
	source.link(true)
	source.client.waitReady()
	source.client.waitStackState(started.StackID, "RunningPrimary")
	source.assertWeb(started.StackID, original)
	if out, err := source.guest("cat " + shellQuote("/"+dataset+"/replicated-data")); err != nil || out != payload {
		t.Fatalf("restore modified original workload: %q %v", out, err)
	}
	t.Log("occupied stack/dataset guards preserved data; original workload still running unchanged")
}

func (n *labGuest) restoreBackupFingerprint(dataset, snapshot string) string {
	n.t.Helper()
	script := "set -eu; zfs get -H -p -o value guid " + shellQuote(dataset+"@"+snapshot) + "; zfs get -H -o property,value readonly,mountpoint,floatlab:backup-source " + shellQuote(dataset)
	out, err := n.guest(script)
	if err != nil {
		n.t.Fatalf("inspect retained backup: %s %v", out, err)
	}
	if !strings.Contains(out, "readonly\ton") || !strings.Contains(out, "mountpoint\tnone") {
		n.t.Fatalf("backup is not read-only and unmounted: %s", out)
	}
	return out
}

func assertRestoredWritable(t *testing.T, node *labGuest, stack, payload string) {
	t.Helper()
	script := "set -eu; container=$(docker ps -q --filter label=com.docker.compose.project=" + shellQuote(stack) + " --filter label=com.docker.compose.service=web); test -n \"$container\"; docker exec \"$container\" sh -ec " + shellQuote("test \"$(cat /data/replicated-data)\" = "+shellQuote(payload)+"; printf %s restored-writable > /data/restore-write; test \"$(cat /data/restore-write)\" = restored-writable")
	if out, err := node.guest(script); err != nil {
		t.Fatalf("restored workload data/writability: %s %v", out, err)
	}
}
