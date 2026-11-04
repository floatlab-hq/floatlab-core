package integration_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func defineBackupRestore(fixture *applianceFixture) {
	var source, backup, remote *labGuest
	var sourceID, backupID, remoteID, compose, snapshot, backupDataset, fingerprint, configPath string
	var started mutationResponse
	var savedCompose []byte
	var checkSavedConfig func() error
	const original = "restore-original"
	const dataset = "floatlab/restore-original/data"
	const payload = "persistent-backup-payload"
	BeforeAll(func(ctx SpecContext) { source, backup, remote = newReplicationLab(integrationT(), fixture) })
	It("captures and transfers a real workload backup and its saved configuration", func(ctx SpecContext) {
		t := integrationT()
		sourceID, backupID, remoteID = source.nodeID(), backup.nodeID(), remote.nodeID()
		source.configurePool(1, sourceID)
		backup.configurePool(2, backupID)
		remote.configurePool(3, remoteID)
		compose = strings.Replace(labWebCompose(original, sourceID, original, source.pool), `volumes: ["data:/data"]`, `volumes: ["./data:/data"]`, 1)
		compose = strings.TrimSuffix(compose, "volumes:\n  data: {}\n")
		started = source.client.startNewStack(original, compose)
		source.client.waitOperation(started.OperationID)
		source.client.waitStackState(started.StackID, "RunningPrimary")
		source.assertWeb(started.StackID, original)
		source.execWeb(started.StackID, "printf %s "+shellQuote(payload)+" > /data/replicated-data")
		if err := source.json("PUT", "/api/v1/storage/replication/"+started.StackID+"/targets", map[string]string{"dataset": dataset, "node_id": backupID, "schedule": "h/24 d/28"}, nil); err != nil {
			t.Fatal(err)
		}
		source.replicateAndWait(started.StackID, dataset, backupID)
		snapshot = source.recoveryPoint(dataset, backupID)
		backupDataset = store.BackupDataset(sourceID, dataset)
		assertSnapshotGUID(t, source, backup, dataset, backupDataset, snapshot)
		fingerprint = backup.restoreBackupFingerprint(backupDataset, snapshot)
		t.Logf("backed up %s@%s with matching source GUID", dataset, snapshot)
		configPath = "/api/v1/storage/backups/config?" + url.Values{"stack_id": {started.StackID}}.Encode()
		var response *http.Response
		var err error
		response, savedCompose, err = source.client.request("GET", configPath, source.client.token, "", "", nil)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("read original backup configuration: %v %s", err, savedCompose)
		}
		checkSavedConfig = func() error {
			response, recovered, err := backup.client.request("GET", configPath, backup.client.token, "", "", nil)
			if err != nil {
				return err
			}
			if response.StatusCode != 200 || string(recovered) != string(savedCompose) {
				return fmt.Errorf("saved backup configuration differs: status=%d body=%s", response.StatusCode, recovered)
			}
			return nil
		}
		waitFor(t, time.Minute, checkSavedConfig)

	})
	It("restores local and remote workloads with the owner offline and protects occupied destinations", func(ctx SpecContext) {
		t := integrationT()
		// The original owner cannot participate in either recovery path.
		source.link(false)
		if err := checkSavedConfig(); err != nil {
			t.Fatalf("read backup configuration with original source offline: %v", err)
		}
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
				"compose_file":        strings.Replace(string(savedCompose), "x-fl-network-pool: "+source.pool, "x-fl-network-pool: "+target.node.pool, 1),
				"snapshots":           []map[string]string{{"dataset": dataset, "snapshot": snapshot}},
			}
			var restored struct {
				OperationID string `json:"operation_id"`
				StackID     string `json:"stack_id"`
				Status      string `json:"status"`
			}
			key := "restore-" + target.name + "-" + fmt.Sprint(time.Now().UnixNano())
			encoded := string(mustJSON(t, request))
			if err := target.node.client.doJSON("POST", "/api/v1/storage/backups/restore", target.node.client.token, "application/json", strings.NewReader(encoded), &restored, key); err != nil {
				t.Fatalf("restore %s: %v", target.name, err)
			}
			if restored.OperationID == "" || restored.StackID == "" || restored.StackID == started.StackID {
				t.Fatalf("restore did not create a separate stack: %+v", restored)
			}
			var repeated struct {
				OperationID string `json:"operation_id"`
				StackID     string `json:"stack_id"`
			}
			if err := target.node.client.doJSON("POST", "/api/v1/storage/backups/restore", target.node.client.token, "application/json", strings.NewReader(encoded), &repeated, key); err != nil || repeated.OperationID != restored.OperationID || repeated.StackID != restored.StackID {
				t.Fatalf("restore idempotency failed: original=%+v repeated=%+v error=%v", restored, repeated, err)
			}
			target.node.client.waitOperation(restored.OperationID)
			target.node.client.waitStackState(restored.StackID, "RunningPrimary")
			target.node.assertWeb(restored.StackID, original)
			assertRestoredWritable(t, target.node, restored.StackID, payload)
			if err := target.node.json("POST", "/api/v1/storage/backups/restore", request, nil); err == nil {
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
		if err := remote.json("POST", "/api/v1/storage/backups/restore", collisionRequest, nil); err == nil {
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
	})
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

func assertRestoredWritable(t testT, node *labGuest, stack, payload string) {
	t.Helper()
	node.execWeb(stack, "test \"$(cat /data/replicated-data)\" = "+shellQuote(payload)+"; printf %s restored-writable > /data/restore-write; test \"$(cat /data/restore-write)\" = restored-writable")
}
