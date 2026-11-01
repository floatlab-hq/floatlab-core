package integration_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/store"
)

// This is deliberately separate from the lab membership suite: all data and
// appliances belong to this test, including rollback and hold cleanup targets.
func TestMultiNodeReplication(t *testing.T) {
	source, backup, other := newReplicationLab(t)
	sourceID, backupID := source.nodeID(), backup.nodeID()
	otherID := other.nodeID()
	source.configurePool(1, sourceID)
	compose := strings.Replace(labWebCompose("replication-data", sourceID, "replication-data", source.pool), `volumes: ["data:/data"]`, "volumes:\n      - type: volume\n        source: data\n        target: /data\n        x-fl-snapshots: h/24 d/28", 1)
	started := source.client.startNewStack("replication-data", compose)
	source.client.waitOperation(started.OperationID)
	source.client.waitStackState(started.StackID, "RunningPrimary")
	stack := started.StackID
	source.assertWeb(stack, "replication-data")
	dataset := "floatlab/replication-data/data"
	backupDataset := store.BackupDataset(sourceID, dataset)
	targetPath := "/api/v1/storage/replication/" + stack + "/targets"
	target := map[string]string{"dataset": dataset, "node_id": backupID, "schedule": "m/240 h/24 d/28"}

	// Seed actual payload bytes and several available points before the first
	// backup; initial seeding must preserve intermediate snapshots too.
	var snapshots []string
	for i := 0; i < 3; i++ {
		if i > 0 {
			// Retention uses actual ZFS creation time. Place points in separate
			// minute buckets without changing appliance clocks or TLS validity.
			time.Sleep(time.Until(time.Now().UTC().Truncate(time.Minute).Add(time.Minute)) + 4*time.Second)
		}
		name, err := store.AutomatedSnapshotName(time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		script := "set -eu; printf %s " + shellQuote(fmt.Sprintf("payload-%d", i)) + " > " + shellQuote("/"+dataset+"/replicated-data") + "; zfs snapshot " + shellQuote(dataset+"@"+name) + "; zfs hold floatlab:acceptance-seed " + shellQuote(dataset+"@"+name)
		if out, err := source.guest(script); err != nil {
			t.Fatalf("seed source snapshot: %s %v", out, err)
		}
		snapshots = append(snapshots, name)
	}
	if err := source.json("PUT", targetPath, target, nil); err != nil {
		t.Fatal(err)
	}
	if err := source.json("PUT", targetPath, map[string]string{"dataset": dataset, "node_id": otherID, "schedule": "m/240 h/24 d/28"}, nil); err != nil {
		t.Fatal(err)
	}
	source.replicateAndWait(stack, dataset, backupID)
	for _, snapshot := range snapshots {
		assertSnapshotGUID(t, source, backup, dataset, backupDataset, snapshot)
	}
	for _, snapshot := range snapshots {
		if out, err := source.guest("zfs release floatlab:acceptance-seed " + shellQuote(dataset+"@"+snapshot)); err != nil {
			t.Fatalf("release test-owned seed hold: %s %v", out, err)
		}
	}
	firstPoint := source.recoveryPoint(dataset, backupID)
	assertRecoveryPoint(t, source, dataset, firstPoint, backupID, true)
	source.replicateAndWait(stack, dataset, otherID)
	assertRecoveryPoint(t, source, dataset, source.recoveryPoint(dataset, otherID), otherID, true)

	// A second run must advance the hold rather than accumulate recovery-point
	// holds, and received data must match a real source snapshot's bytes.
	time.Sleep(3 * time.Second)
	if out, err := source.guest("printf %s second-backup > " + shellQuote("/"+dataset+"/replicated-data")); err != nil {
		t.Fatalf("change source data: %s %v", out, err)
	}
	source.replicateAndWait(stack, dataset, backupID)
	latest := source.recoveryPoint(dataset, backupID)
	if latest == firstPoint {
		t.Fatalf("replication failed to advance from %s", firstPoint)
	}
	assertSnapshotGUID(t, source, backup, dataset, backupDataset, latest)
	prunedSeed := snapshots[1]
	firstTime, _ := store.DecodeSnapshotTimestamp(snapshots[0][3:])
	middleTime, _ := store.DecodeSnapshotTimestamp(snapshots[1][3:])
	if firstTime.Truncate(time.Hour) != middleTime.Truncate(time.Hour) {
		prunedSeed = snapshots[2]
	}
	if out, err := source.guest("if zfs list -H -t snapshot -o name " + shellQuote(dataset+"@"+prunedSeed) + " >/dev/null 2>&1; then exit 1; fi"); err != nil {
		t.Fatalf("source hourly retention failed to prune intermediate minute point: %s %v", out, err)
	}
	if out, err := backup.guest("zfs list -H -t snapshot -o name " + shellQuote(backupDataset+"@"+prunedSeed)); err != nil {
		t.Fatalf("source pruning erased retained backup intermediate: %s %v", out, err)
	}

	// Failed transport raises a destination-specific incident; receiving and
	// verifying a later retry must resolve it.
	if out, err := backup.guest("systemctl stop sshd"); err != nil {
		t.Fatalf("stop owned backup transport: %s %v", out, err)
	}
	t.Cleanup(func() { _, _ = backup.guest("systemctl start sshd") })
	var failed map[string]any
	if err := source.json("POST", "/api/v1/storage/replication/"+stack+"/trigger?"+url.Values{"dataset": {dataset}, "dest_node_id": {backupID}}.Encode(), map[string]any{}, &failed); err != nil {
		t.Fatal(err)
	}
	alertID := "replication:" + sourceID + ":" + dataset + ":" + backupID
	assertReplicationAlert(t, source, stack, alertID, "active")
	if out, err := backup.guest("systemctl start sshd"); err != nil {
		t.Fatalf("restore backup transport: %s %v", out, err)
	}
	source.replicateAndWait(stack, dataset, backupID)
	assertReplicationAlert(t, source, stack, alertID, "resolved")
	latest = source.recoveryPoint(dataset, backupID)
	assertRecoveryPoint(t, source, dataset, latest, backupID, true)
	assertRecoveryPoint(t, source, dataset, firstPoint, backupID, false)
	assertRecoveryPoint(t, source, dataset, source.recoveryPoint(dataset, otherID), otherID, true)
	assertBackupContents(t, backup, backupDataset, latest, "second-backup")
	// Deliberately cut a production SSH receive after part of the stream. The
	// next worker must resume that original point before sending its newer one.
	broken := interruptBackup(t, source, backup, dataset, backupDataset, latest)
	source.replicateAndWait(stack, dataset, backupID)
	assertNoResumeToken(t, backup, backupDataset)
	if out, err := source.guest("zfs release floatlab:acceptance-interrupted " + shellQuote(dataset+"@"+broken)); err != nil {
		t.Fatalf("release resumed target's test hold: %s %v", out, err)
	}
	latest = source.recoveryPoint(dataset, backupID)
	assertSnapshotGUID(t, source, backup, dataset, backupDataset, latest)

	// A deleted original target makes its token unusable. Retry must safely
	// retire partial receive state and recover from the previous common point.
	broken = interruptBackup(t, source, backup, dataset, backupDataset, latest)
	if out, err := source.guest("zfs release floatlab:acceptance-interrupted " + shellQuote(dataset+"@"+broken) + "; zfs destroy " + shellQuote(dataset+"@"+broken)); err != nil {
		t.Fatalf("invalidate interrupted stream target: %s %v", out, err)
	}
	source.replicateAndWait(stack, dataset, backupID)
	assertNoResumeToken(t, backup, backupDataset)
	latest = source.recoveryPoint(dataset, backupID)
	assertSnapshotGUID(t, source, backup, dataset, backupDataset, latest)

	// Removing a destination must release its recovery-point hold, immediately
	// prune the source, and leave the received backup history recoverable.
	if err := source.json("DELETE", targetPath, map[string]string{"dataset": dataset, "node_id": backupID}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Minute, func() error {
		out, err := source.guest(snapshotHoldsCommand(dataset))
		if err != nil {
			return err
		}
		if strings.Contains(out, backupID) {
			return fmt.Errorf("removed destination still has holds: %s", out)
		}
		return nil
	})
	assertRecoveryPoint(t, source, dataset, source.recoveryPoint(dataset, otherID), otherID, true)
	if out, err := backup.guest("zfs list -H -t snapshot -o name " + shellQuote(backupDataset+"@"+latest)); err != nil {
		t.Fatalf("destination removal destroyed received backup: %s %v", out, err)
	}
	var removed map[string]any
	if err := source.json("POST", "/api/v1/lab/members/"+otherID+"/remove", map[string]string{"confirm_node_id": otherID, "operation_id": fmt.Sprintf("remove-backup-%d", time.Now().UnixNano())}, &removed); err != nil {
		t.Fatalf("remove remaining backup node: %v", err)
	}
	waitFor(t, time.Minute, func() error {
		out, err := source.guest(snapshotHoldsCommand(dataset))
		if err != nil {
			return err
		}
		if strings.Contains(out, otherID) {
			return fmt.Errorf("removed node still has holds: %s", out)
		}
		return nil
	})
}

func newReplicationLab(t *testing.T) (*labGuest, *labGuest, *labGuest) {
	t.Helper()
	if os.Getenv("FLOATLAB_MULTI_NODE_INTEGRATION") != "1" {
		t.Skip("run scripts/test-multi-node.sh for real ZFS replication acceptance")
	}
	if os.Getenv("API_URL") != "" || os.Getenv("VM_NAME") != "" || os.Getenv("VM_SSH") != "" {
		t.Fatal("replication acceptance requires fresh owned appliances")
	}
	if os.Getenv("FLOATLAB_MULTI_NODE_RUN_ID") == "" {
		t.Setenv("FLOATLAB_MULTI_NODE_RUN_ID", fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	source := newLabGuest(t, 1)
	source.refreshReplicationRuntime()
	backup := newLabGuest(t, 2)
	backup.refreshReplicationRuntime()
	backup.join(source)
	assertLabMembers(t, 2, source, backup)
	other := newLabGuest(t, 3)
	other.refreshReplicationRuntime()
	other.join(source)
	assertLabMembers(t, 3, source, backup, other)
	if os.Getenv("FLOATLAB_MULTI_NODE_MANAGEMENT_ARCHIVE") != "" {
		for _, node := range []*labGuest{source, backup, other} {
			out, err := node.guest("set -eu; sed -i 's|exec floatlab-hostd backup-receive|exec /floatlab/system/updates/appliance-hostd-current backup-receive|' /floatlab/system/etc/lab/replica-receive-root.sh; grep -F 'exec /floatlab/system/updates/appliance-hostd-current backup-receive' /floatlab/system/etc/lab/replica-receive-root.sh")
			if err != nil {
				t.Fatalf("activate current SSH receiver on %s: %s %v", node.name, out, err)
			}
		}
	}
	return source, backup, other
}

// Reuse an existing disposable ISO while validating freshly built production
// control and hostd code. The archive is supplied by the acceptance runner.
func (n *labGuest) refreshReplicationRuntime() {
	archive := os.Getenv("FLOATLAB_MULTI_NODE_MANAGEMENT_ARCHIVE")
	if archive == "" {
		return
	}
	const remote = "/floatlab/system/updates/replication-management.tar.gz"
	uploadUpgradeGuestFile(n.t, n.root, archive, remote)
	if out, err := n.guest("set -eu; docker load -i " + shellQuote(remote) + "; sed -i 's|image: floatlab-bundle/floatlab-control:[a-f0-9]*|image: floatlab-manage:dev|' /floatlab/system/docker-compose.yml; docker compose -f /floatlab/system/docker-compose.yml up -d --force-recreate floatlab-control; docker inspect --format '{{.Image}}' $(docker ps -q --filter label=com.docker.compose.project=floatlab-system --filter label=com.docker.compose.service=floatlab-control)"); err != nil {
		n.t.Fatalf("load current production management image: %s %v", out, err)
	} else {
		n.t.Logf("%s production management runtime: %s", n.name, strings.TrimSpace(out))
	}
	installCurrentUpgradeHostd(n.t, n.root)
	n.client.waitReady()
}

func assertReplicationAlert(t *testing.T, source *labGuest, stack, id, state string) {
	t.Helper()
	waitFor(t, time.Minute, func() error {
		var rows []struct{ ID, State string }
		if err := source.json("GET", "/api/v1/alerts?stack_id="+url.QueryEscape(stack), nil, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == id && row.State == state {
				return nil
			}
		}
		return fmt.Errorf("replication incident %s has not reached %s: %+v", id, state, rows)
	})
}

func interruptBackup(t *testing.T, source, backup *labGuest, dataset, backupDataset, base string) string {
	t.Helper()
	time.Sleep(3 * time.Second)
	if time.Now().UTC().Second() < 3 {
		time.Sleep(3 * time.Second)
	}
	name, err := store.AutomatedSnapshotName(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	script := "set -eu; dd if=/dev/urandom of=" + shellQuote("/"+dataset+"/interrupted-data") + " bs=1M count=24 status=none; zfs snapshot " + shellQuote(dataset+"@"+name) + "; zfs hold floatlab:acceptance-interrupted " + shellQuote(dataset+"@"+name)
	if out, err := source.guest(script); err != nil {
		t.Fatalf("seed interrupted stream: %s %v", out, err)
	}
	// The SSH receiver is the actual restricted production entry point. Only
	// the test-owned sender truncates its input, simulating a lost connection.
	script = "zfs send -I " + shellQuote(dataset+"@"+base) + " " + shellQuote(dataset+"@"+name) + " | head -c 1048576 | ssh -o StrictHostKeyChecking=yes -o BatchMode=yes -o IdentitiesOnly=yes -o UserKnownHostsFile=/floatlab/system/workloads/lab/known_hosts -i /floatlab/system/workloads/lab/replication-key " + shellQuote("floatlab-replica@"+backup.host()) + " zfs-backup receive " + shellQuote(backupDataset) + " -"
	if out, err := source.guest(script); err == nil {
		t.Fatalf("truncated replication unexpectedly succeeded: %s", out)
	}
	out, err := backup.guest("zfs get -H -o value receive_resume_token " + shellQuote(backupDataset))
	if err != nil || strings.TrimSpace(out) == "" || strings.TrimSpace(out) == "-" {
		t.Fatalf("interrupted receive did not save resume state: %s %v", out, err)
	}
	if validation, err := source.guest("zfs send -nvt " + shellQuote(strings.TrimSpace(out))); err != nil {
		t.Fatalf("interrupted stream did not produce a resumable token: %s %v", validation, err)
	}
	return name
}

func assertNoResumeToken(t *testing.T, backup *labGuest, dataset string) {
	t.Helper()
	out, err := backup.guest("zfs get -H -o value receive_resume_token " + shellQuote(dataset))
	if err != nil || strings.TrimSpace(out) != "-" {
		t.Fatalf("successful replication left receive resume state: %s %v", out, err)
	}
}

func (n *labGuest) replicateAndWait(stack, dataset, target string) {
	n.t.Helper()
	time.Sleep(3 * time.Second)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	path := "/api/v1/storage/replication/" + stack + "/trigger?" + url.Values{"dataset": {dataset}, "dest_node_id": {target}}.Encode()
	if err := n.json("POST", path, map[string]any{}, &accepted); err != nil {
		n.t.Fatal(err)
	}
	if accepted.TaskID == "" {
		n.t.Fatal("replication trigger returned no task ID")
	}
	waitFor(n.t, 3*time.Minute, func() error {
		var rows []struct{ ID, State, Error string }
		if err := n.json("GET", "/api/v1/storage/replication?stack_id="+url.QueryEscape(stack), nil, &rows); err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID != accepted.TaskID {
				continue
			}
			if row.State == "complete" {
				return nil
			}
			if row.State == "failed" {
				n.t.Fatalf("replication failed: %s", row.Error)
			}
			return fmt.Errorf("replication state: %s (%s)", row.State, row.Error)
		}
		return fmt.Errorf("replication task %s not visible", accepted.TaskID)
	})
}

func snapshotHoldsCommand(dataset string) string {
	return "set -eu; for snapshot in $(zfs list -H -t snapshot -o name -r " + shellQuote(dataset) + "); do zfs holds -H \"$snapshot\"; done"
}

func (n *labGuest) recoveryPoint(dataset, target string) string {
	n.t.Helper()
	out, err := n.guest(snapshotHoldsCommand(dataset))
	if err != nil {
		n.t.Fatalf("list recovery-point holds: %s %v", out, err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "floatlab:hold:"+target && strings.HasPrefix(fields[0], dataset+"@") {
			names = append(names, strings.TrimPrefix(fields[0], dataset+"@"))
		}
	}
	if len(names) != 1 {
		n.t.Fatalf("target %s has %d recovery points, want exactly one: %s", target, len(names), out)
	}
	return names[0]
}

func assertSnapshotGUID(t *testing.T, source, destination *labGuest, dataset, backupDataset, snapshot string) {
	t.Helper()
	command := "zfs get -H -p -o value guid " + shellQuote(dataset+"@"+snapshot)
	a, err := source.guest(command)
	if err != nil {
		t.Fatalf("source GUID: %s %v", a, err)
	}
	b, err := destination.guest("zfs get -H -p -o value guid " + shellQuote(backupDataset+"@"+snapshot))
	if err != nil || strings.TrimSpace(a) != strings.TrimSpace(b) {
		t.Fatalf("received snapshot GUID differs: source=%q destination=%q error=%v", a, b, err)
	}
}

func assertBackupContents(t *testing.T, backup *labGuest, dataset, snapshot, expected string) {
	t.Helper()
	clone := "floatlab/replication-verification"
	mount := "/tmp/floatlab-replication-verification"
	script := "set -eu; zfs clone -o readonly=on -o mountpoint=" + shellQuote(mount) + " " + shellQuote(dataset+"@"+snapshot) + " " + shellQuote(clone) + "; trap 'zfs destroy floatlab/replication-verification' EXIT; cat " + shellQuote(mount+"/replicated-data")
	out, err := backup.guest(script)
	if err != nil || out != expected {
		t.Fatalf("received data mismatch: %q, expected %q: %v", out, expected, err)
	}
}

func assertRecoveryPoint(t *testing.T, source *labGuest, dataset, snapshot, target string, held bool) {
	t.Helper()
	full := dataset + "@" + snapshot
	out, err := source.guest("if zfs list -H -t snapshot -o name " + shellQuote(full) + " >/dev/null 2>&1; then zfs get -H -o value floatlab:hold-targets " + shellQuote(full) + "; zfs holds -H " + shellQuote(full) + "; fi")
	if err != nil {
		t.Fatalf("inspect recovery point: %s %v", out, err)
	}
	metadata, tags, _ := strings.Cut(strings.TrimSpace(out), "\n")
	var nodes []string
	if metadata != "" && metadata != "-" {
		if err := json.Unmarshal([]byte(metadata), &nodes); err != nil {
			t.Fatalf("invalid snapshot hold metadata: %s %v", metadata, err)
		}
	}
	referenced := false
	for _, node := range nodes {
		referenced = referenced || node == target
	}
	if referenced != held || strings.Contains(tags, "floatlab:hold:"+target) != held {
		t.Fatalf("recovery point %s target %s held=%t: metadata=%s tags=%s", full, target, held, metadata, tags)
	}
}
