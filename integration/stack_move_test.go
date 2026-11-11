package integration_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
)

const stackMoveStopFile = "/clean-shutdown-marker"
const stackMoveStopMarker = "WebDAV stopped cleanly"

func defineStackMove(fixture *applianceFixture) {
	var source, destination *labGuest
	var sourceID, destinationID string
	BeforeAll(func(ctx SpecContext) {
		t := integrationT()
		source, destination = newLabGuest(t, fixture, 1), newLabGuest(t, fixture, 2)
		destination.join(source)
		assertLabMembers(t, 2, source, destination)
		sourceID, destinationID = source.nodeID(), destination.nodeID()
		source.configurePool(1, sourceID)
		destination.configurePool(2, destinationID)
	})
	DescribeTable("moves a running WebDAV stack and replicates back to its former host",
		func(ctx SpecContext, name string, preseed bool) {
			t := integrationT()
			dataset := "floatlab/" + name + "/data"
			started := source.client.startNewStack(name, stackMoveWebDAVCompose(name, sourceID, source.pool))
			source.client.waitOperation(started.OperationID)
			source.client.waitStackState(started.StackID, "RunningPrimary")
			stackPath := "/api/v1/stacks/" + started.StackID
			targetsPath := "/api/v1/storage/replication/" + started.StackID + "/targets"
			sourceAddress, err := stackMoveAddress(source, stackPath)
			if err != nil || !sourceAddress.Is4() || sourceAddress.Compare(source.firstIP) < 0 || sourceAddress.Compare(source.lastIP) > 0 {
				t.Fatalf("source workload address %s is outside the source pool: %v", sourceAddress, err)
			}
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			webdav := &http.Client{Transport: transport, Timeout: 700 * time.Millisecond}
			request := func(method string, address netip.Addr, path string, body io.Reader) (int, string, error) {
				req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort(address.String(), "8088")+path, body)
				if err != nil {
					return 0, "", err
				}
				req.Header.Set("Depth", "0")
				resp, err := webdav.Do(req)
				if err != nil {
					return 0, "", err
				}
				defer resp.Body.Close()
				data, err := io.ReadAll(resp.Body)
				return resp.StatusCode, string(data), err
			}
			put := func(address netip.Addr, path, content string) {
				t.Helper()
				status, body, err := request(http.MethodPut, address, path, strings.NewReader(content))
				if err != nil || status != http.StatusCreated {
					t.Fatalf("WebDAV PUT %s%s: status=%d body=%q error=%v", address, path, status, body, err)
				}
			}
			waitFor(t, time.Minute, func() error {
				status, _, err := request(http.MethodGet, sourceAddress, "/", nil)
				if err != nil || status != http.StatusOK {
					return fmt.Errorf("source WebDAV readiness: HTTP %d: %v", status, err)
				}
				return nil
			})
			if preseed {
				put(sourceAddress, "/before-replication", "present at the initial replication point")
				if err := source.json(http.MethodPut, targetsPath, map[string]string{"dataset": dataset, "node_id": destinationID, "schedule": "m/240 h/24 d/28"}, nil); err != nil {
					t.Fatal(err)
				}
				source.replicateAndWait(started.StackID, dataset, destinationID)
				point := source.recoveryPoint(dataset, destinationID)
				assertSnapshotGUID(t, source, destination, dataset, store.BackupDataset(sourceID, dataset), point)
			}
			file := "/" + uuid.NewString()
			payload := "floatlab-webdav-" + uuid.NewString()
			put(sourceAddress, file, payload)
			if status, body, err := request("PROPFIND", sourceAddress, file, nil); err != nil || status != http.StatusMultiStatus || !strings.Contains(body, file) {
				t.Fatalf("WebDAV PROPFIND %s: status=%d body=%q error=%v", file, status, body, err)
			}
			if status, _, err := request(http.MethodGet, sourceAddress, stackMoveStopFile, nil); err != nil || status != http.StatusNotFound {
				t.Fatalf("shutdown marker should be absent before moving: status=%d error=%v", status, err)
			}
			caller := source
			if preseed {
				caller = destination
				waitFor(t, time.Minute, func() error {
					_, err := stackMoveAddress(caller, stackPath)
					return err
				})
			}
			var address atomic.Value
			address.Store(sourceAddress)
			probeContext, stop := context.WithCancel(ctx)
			defer stop()
			probeReady, probeRecovered := make(chan struct{}), make(chan struct{})
			type samples struct {
				before, missed, after int
				invalid               string
			}
			result := make(chan samples, 1)
			go func() {
				ticker := time.NewTicker(100 * time.Millisecond)
				defer ticker.Stop()
				observed := samples{}
				defer func() { result <- observed }()
				sample := func() {
					current := address.Load().(netip.Addr)
					status, body, err := request(http.MethodGet, current, file, nil)
					switch {
					case err != nil || status != http.StatusOK:
						observed.missed++
					case body != payload:
						observed.invalid = fmt.Sprintf("expected %q, received %q", payload, body)
					case current == sourceAddress:
						observed.before++
					default:
						observed.after++
						if observed.after == 1 {
							close(probeRecovered)
						}
					}
				}
				sample()
				close(probeReady)
				for {
					select {
					case <-probeContext.Done():
						return
					case <-ticker.C:
						sample()
					}
				}
			}()
			<-probeReady
			moveRequest := map[string]string{"destination_node_id": destinationID}
			if !preseed {
				moveRequest["network_pool"] = destination.pool
			}
			move := caller.client.mutateJSON(http.MethodPost, stackPath+"/move", moveRequest)
			if move.OperationID == "" {
				t.Fatal("move returned no operation ID")
			}
			var checkpoint string
			waitFor(t, 10*time.Minute, func() error {
				if candidate, err := stackMoveAddress(destination, stackPath); err == nil && candidate.Is4() &&
					candidate.Compare(destination.firstIP) >= 0 && candidate.Compare(destination.lastIP) <= 0 {
					address.Store(candidate)
				}
				var operation struct {
					State, Checkpoint, Error string
				}
				if err := caller.json(http.MethodGet, "/api/v1/operations/"+move.OperationID, nil, &operation); err != nil {
					return err
				}
				if operation.Checkpoint != checkpoint {
					checkpoint = operation.Checkpoint
					t.Logf("move %s: %s (%s)", move.OperationID, operation.State, checkpoint)
				}
				switch operation.State {
				case "failed":
					t.Fatalf("move failed at %s: %s", checkpoint, operation.Error)
				case "succeeded":
					if address.Load().(netip.Addr) != sourceAddress {
						return nil
					}
				}
				return fmt.Errorf("move state=%s checkpoint=%s error=%s address=%s", operation.State, checkpoint, operation.Error, address.Load())
			})

			destinationAddress := address.Load().(netip.Addr)
			destination.client.waitStackState(started.StackID, "RunningPrimary")
			for _, entry := range []*labGuest{source, destination} {
				var stack struct {
					ID          string
					PrimaryNode string `json:"primary_node"`
				}
				if err := entry.json(http.MethodGet, stackPath, nil, &stack); err != nil || stack.ID != started.StackID || stack.PrimaryNode != destinationID {
					t.Fatalf("%s did not route the original stack to its new owner: %+v %v", entry.name, stack, err)
				}
			}
			if status, body, err := request(http.MethodGet, destinationAddress, file, nil); err != nil || status != http.StatusOK || body != payload {
				t.Fatalf("moved WebDAV file %s: status=%d body=%q error=%v", file, status, body, err)
			}
			if status, body, err := request(http.MethodGet, destinationAddress, stackMoveStopFile, nil); err != nil || status != http.StatusOK || body != stackMoveStopMarker {
				t.Fatalf("final sync did not preserve the shutdown write: status=%d body=%q error=%v", status, body, err)
			}
			select {
			case <-probeRecovered:
			case <-time.After(3 * time.Second):
				t.Fatal("periodic WebDAV probes did not recover after cutover")
			}
			stop()
			observed := <-result
			t.Logf("WebDAV GETs before cutover=%d unsuccessful=%d after cutover=%d", observed.before, observed.missed, observed.after)
			if observed.before == 0 || observed.missed == 0 || observed.after == 0 || observed.invalid != "" {
				t.Fatalf("expected correct WebDAV data on both hosts with a brief outage: %+v", observed)
			}
			if status, _, err := request(http.MethodGet, sourceAddress, file, nil); err == nil && status == http.StatusOK {
				t.Fatalf("former workload endpoint %s still serves %s", sourceAddress, file)
			}
			for _, host := range []*labGuest{source, destination} {
				out, err := host.guest("docker ps --quiet --filter " + shellQuote("label=com.docker.compose.project="+started.StackID))
				if err != nil || (strings.TrimSpace(out) != "") != (host == destination) {
					t.Fatalf("%s has unexpected running containers for %s: %q %v", host.name, started.StackID, out, err)
				}
			}
			archive := "floatlab/restores/moves/" + move.OperationID + "/data"
			snapshots, err := source.guest("zfs list -H -t snapshot -o name -s createtxg -d 1 " + shellQuote(archive))
			if err != nil {
				t.Fatalf("read final source snapshots: %s %v", snapshots, err)
			}
			points := strings.Fields(snapshots)
			if len(points) == 0 {
				t.Fatalf("move archived no source snapshots: %q", snapshots)
			}
			final, owned := strings.CutPrefix(points[len(points)-1], archive+"@")
			if !owned || !store.IsAutomatedSnapshot(final) {
				t.Fatalf("expected a final source snapshot at %s: %q", archive, snapshots)
			}
			assertSnapshotGUID(t, source, destination, archive, dataset, final)

			var targets []config.BackupTarget
			if err := destination.json(http.MethodGet, targetsPath, nil, &targets); err != nil {
				t.Fatal(err)
			}
			reversed := false
			for _, target := range targets {
				if target.Dataset == dataset && target.Enabled {
					if target.NodeID != sourceID || target.SourceNodeID != destinationID || target.Schedule == "" {
						t.Fatalf("incorrect enabled target after the move: %+v", target)
					}
					reversed = true
				}
			}
			if !reversed {
				t.Fatalf("former primary is missing as a backup target: %+v", targets)
			}
			destination.replicateAndWait(started.StackID, dataset, sourceID)
			point := destination.recoveryPoint(dataset, sourceID)
			assertSnapshotGUID(t, destination, source, dataset, store.BackupDataset(destinationID, dataset), point)
		},
		Entry("without a replica", "webdav-move-unseeded", false),
		Entry("with an existing replica older than the uploaded file", "webdav-move-preseeded", true),
	)
}

func stackMoveAddress(node *labGuest, path string) (netip.Addr, error) {
	var status struct {
		Address string `json:"stack_ip"`
	}
	if err := node.json(http.MethodGet, path+"/status", nil, &status); err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(status.Address)
}

func stackMoveWebDAVCompose(name, node, pool string) string {
	return fmt.Sprintf(`name: %s
x-fl-network-pool: %s
x-fl-stack:
  schema_version: 1
  primary_node: %s
  storage:
    pool: floatlab
services:
  webdav:
    image: rclone/rclone:1.70.3
    entrypoint: ["/bin/sh", "-ec"]
    command:
      - |
        stop_server() {
          kill -TERM "$!" 2>/dev/null || true
          wait "$!" || true
          printf '%%s' '%s' > /data%s
          exit 0
        }
        trap stop_server TERM
        rclone serve webdav /data --addr :8080 &
        wait "$!"
    volumes: ["data:/data"]
    ports: ["8088:8080"]
    healthcheck:
      test: ["CMD", "wget", "-qO", "/dev/null", "http://127.0.0.1:8080/"]
      interval: 2s
      timeout: 2s
      retries: 30
volumes:
  data: {}
`, name, pool, node, stackMoveStopMarker, stackMoveStopFile)
}

func TestStackMoveWebDAVFixture(t *testing.T) {
	const name = "webdav-move"
	source := stackMoveWebDAVCompose(name, "node-a", "test-pool")
	if _, err := compose.ParseAndValidate(t.Context(), source, name); err != nil {
		t.Fatal(err)
	}
	runtime, err := compose.RuntimeYAML(source, name, "198.19.1.128")
	if err != nil || !strings.Contains(runtime, `kill -TERM "$!"`) {
		t.Fatalf("runtime Compose must preserve the WebDAV shutdown handler: %v\n%s", err, runtime)
	}
}
