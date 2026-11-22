package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

func defineManualFailover(fixture *applianceFixture, nodeCount int) {
	It("recovers a verified replica with the primary powered off and safely boots the former primary", func(ctx SpecContext) {
		t := integrationT()
		By("provisioning disposable appliances and assigning their LAN address pools")
		primary, secondary := newLabGuest(t, fixture, 1), newLabGuest(t, fixture, 2)
		secondary.join(primary)
		assertLabMembers(t, 2, primary, secondary)
		var witness *labGuest
		if nodeCount == 3 {
			witness = newLabGuest(t, fixture, 3)
			witness.join(primary)
			assertLabMembers(t, 3, primary, secondary, witness)
		}
		primaryID, secondaryID := primary.nodeID(), secondary.nodeID()
		primary.configurePool(1, primaryID)
		secondary.configurePool(2, secondaryID)

		const name = "manual-failover-web"
		const cloneName = "manual-failover-clone"
		dataset := "floatlab/" + name + "/data"
		sourceCompose := labWebCompose(name, primaryID, name, primary.pool)
		sourceCompose = strings.Replace(sourceCompose, "  failover:", "  secondary_node: "+secondaryID+"\n  failover:", 1)
		sourceCompose = strings.Replace(sourceCompose, "    image:", "    restart: unless-stopped\n    image:", 1)
		sourceCompose = strings.Replace(sourceCompose, "exec caddy", "echo boot >> /data/boot-count; exec caddy", 1)
		created := primary.client.object(http.MethodPost, "/api/v1/stacks", map[string]string{"name": name, "compose_file": sourceCompose}, http.StatusCreated)
		stackID := created["id"].(string)
		stackPath := "/api/v1/stacks/" + stackID
		primary.client.waitStackState(stackID, "Idle")
		started := primary.client.mutate(http.MethodPost, stackPath+"/start", nil, "application/json")
		primary.client.waitOperation(started.OperationID)
		primary.client.waitStackState(stackID, "RunningPrimary")
		primary.assertWeb(stackID, name)
		originalIP, err := stackMoveAddress(primary, stackPath)
		if err != nil {
			t.Fatal(err)
		}
		originalMAC := networkNeighborMAC(t, secondary, originalIP)

		failoverPath := "/api/v1/failover/" + stackID
		previewPath := failoverPath + "/preview?" + url.Values{"destination_node_id": {secondaryID}}.Encode()
		triggerPath := failoverPath + "/trigger"
		waitFor(t, time.Minute, func() error {
			var workload struct {
				Secondary string `json:"secondary_node"`
			}
			if err := secondary.json(http.MethodGet, stackPath, nil, &workload); err != nil {
				return err
			}
			if workload.Secondary != secondaryID {
				return fmt.Errorf("expected a nominated secondary, got %q", workload.Secondary)
			}
			return nil
		})
		rejectManualFailover(t, secondary, http.MethodGet, previewPath, nil, http.StatusConflict)

		By("replicating known data before writing a second, unreplicated file")
		primary.execWeb(stackID, "printf %s verified-bytes > /data/checkpoint")
		targetsPath := "/api/v1/storage/replication/" + stackID + "/targets"
		if err := primary.json(http.MethodPut, targetsPath, map[string]string{
			"dataset": dataset, "node_id": secondaryID, "schedule": "m/240 h/24 d/28",
		}, nil); err != nil {
			t.Fatal(err)
		}
		primary.replicateAndWait(stackID, dataset, secondaryID)
		snapshot := primary.recoveryPoint(dataset, secondaryID)
		backupDataset := store.BackupDataset(primaryID, dataset)
		assertSnapshotGUID(t, primary, secondary, dataset, backupDataset, snapshot)
		primary.execWeb(stackID, "printf %s original-only > /data/after-replication")
		if out, err := primary.guest("sync"); err != nil {
			t.Fatalf("flush existing primary writes before fencing: %s %v", out, err)
		}

		By("fencing the primary with a hard power-off and confirming the recovery point")
		checkpointGuest(t, primary.vm)
		powerCtx, cancel := context.WithTimeout(ctx, time.Minute)
		err = primary.vm.PowerOff(powerCtx)
		cancel()
		if err != nil {
			t.Fatalf("power off the primary: %v", err)
		}
		primaryOff := true
		t.Cleanup(func() {
			if primaryOff {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if err := primary.vm.PowerOn(cleanupCtx); err != nil {
					t.Errorf("restore VM power for diagnostics and coverage: %v", err)
				}
			}
		})

		var preview struct {
			StackID      string    `json:"stack_id"`
			Source       string    `json:"source_node_id"`
			Destination  string    `json:"destination_node_id"`
			Mode         string    `json:"mode"`
			Token        string    `json:"recovery_point"`
			Snapshot     string    `json:"snapshot"`
			SnapshotAt   time.Time `json:"snapshot_at"`
			LossSeconds  int       `json:"potential_data_loss_seconds"`
			NeedsNetwork bool      `json:"requires_network"`
			Datasets     []struct {
				Dataset  string `json:"dataset"`
				Snapshot string `json:"snapshot"`
			}
		}
		waitFor(t, time.Minute, func() error {
			if err := secondary.json(http.MethodGet, previewPath, nil, &preview); err != nil {
				return err
			}
			if preview.LossSeconds <= 0 {
				return fmt.Errorf("loss estimate has not advanced: %d", preview.LossSeconds)
			}
			return nil
		})
		mode := "takeover"
		if nodeCount == 2 {
			mode = "clone"
		}
		if preview.StackID != stackID || preview.Source != primaryID || preview.Destination != secondaryID ||
			preview.Mode != mode || preview.Token == "" || preview.Snapshot != snapshot || preview.SnapshotAt.IsZero() ||
			!preview.NeedsNetwork || len(preview.Datasets) != 1 ||
			preview.Datasets[0].Dataset != dataset || preview.Datasets[0].Snapshot != snapshot {
			t.Fatalf("preview disagrees with the verified replica or expected recovery mode: %+v", preview)
		}
		t.Logf("verified %s preview for %s with potential data loss of %d seconds", mode, snapshot, preview.LossSeconds)

		request := map[string]any{"destination_node_id": secondaryID, "network_pool": secondary.pool, "recovery_point": preview.Token}
		if nodeCount == 2 {
			request["name"] = cloneName
		}
		rejectManualFailover(t, secondary, http.MethodPost, triggerPath, request, http.StatusBadRequest)
		request["primary_fenced"] = true
		request["recovery_point"] = "unconfirmed-recovery-point"
		rejectManualFailover(t, secondary, http.MethodPost, triggerPath, request, http.StatusConflict)
		request["recovery_point"] = preview.Token
		if nodeCount == 2 {
			request["name"] = name
			rejectManualFailover(t, secondary, http.MethodPost, triggerPath, request, http.StatusBadRequest)
			request["name"] = cloneName
			request["network_pool"] = primary.pool
			rejectManualFailover(t, secondary, http.MethodPost, triggerPath, request, http.StatusConflict)
			request["network_pool"] = secondary.pool
		}
		assertLocalProject(t, secondary, stackID, false)

		By("starting from the verified replica and checking data and ownership")
		accepted := secondary.client.mutateJSON(http.MethodPost, triggerPath, request)
		if accepted.StackID == "" || (accepted.StackID != stackID) != (nodeCount == 2) {
			t.Fatalf("wrong recovered stack identity for %s: %+v", mode, accepted)
		}
		secondary.client.waitOperation(accepted.OperationID)
		secondary.client.waitStackState(accepted.StackID, "RunningPrimary")
		assertLocalProject(t, secondary, accepted.StackID, true)
		secondary.assertWeb(accepted.StackID, name)
		if out := secondary.execWeb(accepted.StackID, "test ! -e /data/after-replication; cat /data/checkpoint"); out != "verified-bytes" {
			t.Fatalf("recovered data differs from the verified checkpoint: %q", out)
		}
		secondary.execWeb(accepted.StackID, "printf %s secondary-only > /data/after-takeover")

		By("booting the former primary after recovery")
		if nodeCount == 2 {
			// Keep the other node unreachable throughout the original's boot.
			secondary.link(false)
		}
		powerCtx, cancel = context.WithTimeout(ctx, time.Minute)
		err = primary.vm.PowerOn(powerCtx)
		cancel()
		if err != nil {
			t.Fatalf("boot the former primary: %v", err)
		}
		primaryOff = false
		primary.client.waitReady()

		if nodeCount == 2 {
			primary.client.waitStackState(stackID, "RunningPrimary")
			primary.assertWeb(stackID, name)
			if out := primary.execWeb(stackID, "test ! -e /data/after-takeover; cat /data/after-replication"); out != "original-only" {
				t.Fatalf("original workload and clone share or lost data: %q", out)
			}
			secondary.link(true)
			assertFailoverOwner(t, primary, stackID, primaryID, name)
			assertFailoverOwner(t, secondary, accepted.StackID, secondaryID, cloneName)
			cloneIP, err := stackMoveAddress(secondary, "/api/v1/stacks/"+accepted.StackID)
			if err != nil || cloneIP == originalIP {
				t.Fatalf("clone must use a different LAN IP, original=%s clone=%s: %v", originalIP, cloneIP, err)
			}
			if cloneMAC := networkNeighborMAC(t, primary, cloneIP); cloneMAC == originalMAC {
				t.Fatalf("clone and original both use LAN MAC %s", cloneMAC)
			}
			assertLocalProject(t, primary, stackID, true)
			assertLocalProject(t, secondary, accepted.StackID, true)
			return
		}

		for _, node := range []*labGuest{witness, primary, secondary} {
			assertFailoverOwner(t, node, stackID, secondaryID, name)
		}
		// The startup marker catches a transient Docker auto-start even if the
		// returning node later discovers the new owner and stops the container.
		localProject := "docker ps -q --filter " + shellQuote("label=com.docker.compose.project="+stackID)
		gomega.NewWithT(t).Consistently(ctx, func() error {
			out, err := primary.guest("set -eu; test -z \"$(" + localProject + ")\"; mount=$(zfs get -H -o value mountpoint " + shellQuote(dataset) + "); cat \"$mount/boot-count\"")
			if err != nil || strings.TrimSpace(out) != "boot" {
				return fmt.Errorf("former primary started a duplicate: output=%q error=%v", out, err)
			}
			return nil
		}).WithTimeout(15 * time.Second).WithPolling(2 * time.Second).Should(gomega.Succeed())
		assertLocalProject(t, secondary, accepted.StackID, true)
	})
}

func rejectManualFailover(t testT, node *labGuest, method, path string, payload map[string]any, want int) {
	t.Helper()
	var response *http.Response
	var data []byte
	var err error
	if payload == nil {
		response, data, err = node.client.request(method, path, node.client.token, "", "", nil)
	} else {
		response, data, err = node.client.request(method, path, node.client.token, uuid.NewString(), "application/json", bytes.NewReader(mustJSON(t, payload)))
	}
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s returned %s, expected HTTP %d: %s", method, path, response.Status, want, data)
	}
}

func assertFailoverOwner(t testT, observer *labGuest, stackID, owner, name string) {
	t.Helper()
	waitFor(t, time.Minute, func() error {
		var stack struct {
			ID, Name string
			Owner    string `json:"primary_node"`
		}
		if err := observer.json(http.MethodGet, "/api/v1/stacks/"+stackID, nil, &stack); err != nil {
			return err
		}
		if stack.ID != stackID || stack.Owner != owner || stack.Name != name {
			return fmt.Errorf("%s reports the wrong recovered stack: %+v", observer.name, stack)
		}
		return nil
	})
}

func assertLocalProject(t testT, node *labGuest, stackID string, running bool) {
	t.Helper()
	out, err := node.guest("docker ps -q --filter " + shellQuote("label=com.docker.compose.project="+stackID))
	if err != nil || (strings.TrimSpace(out) != "") != running {
		t.Fatalf("%s local Docker project %s running=%t: %q %v", node.name, stackID, running, out, err)
	}
}

func networkNeighborMAC(t testT, observer *labGuest, address netip.Addr) string {
	t.Helper()
	var mac net.HardwareAddr
	waitFor(t, time.Minute, func() error {
		out, err := observer.guest("curl -fsS --max-time 4 " + shellQuote("http://"+net.JoinHostPort(address.String(), "8088")+"/") +
			" >/dev/null && ip neigh show " + shellQuote(address.String()))
		if err != nil {
			return fmt.Errorf("probe %s: %q %w", address, out, err)
		}
		fields := strings.Fields(out)
		for i := 1; i < len(fields); i++ {
			if fields[i-1] == "lladdr" {
				mac, err = net.ParseMAC(fields[i])
				return err
			}
		}
		return fmt.Errorf("no neighbor MAC for %s: %s", address, out)
	})
	return mac.String()
}
