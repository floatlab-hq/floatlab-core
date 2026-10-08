package integration_test

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func (c *apiClient) assertRecoveryWorkload() {
	t := c.t
	name := fmt.Sprintf("recovery-%d", time.Now().UnixNano())
	source := composeFixture(name, name, "alpine:3.20", "v1")
	// Keep the fixture's data file stable across container recreation.
	source = strings.Replace(source, "echo v1 > /data/revision.txt", "test -f /data/revision.txt || echo v1 > /data/revision.txt", 1)
	source = strings.Replace(source, "x-fl-health-timeout: 90s", "x-fl-health-timeout: 15s", 1)
	source = strings.Replace(source, "    volumes:\n", "    healthcheck:\n      test: [\"CMD-SHELL\", \"grep -q '3.20' /etc/alpine-release\"]\n      interval: 1s\n      timeout: 1s\n      retries: 1\n    volumes:\n", 1)
	c.expect("POST", "/api/v1/stacks/validate", map[string]string{"name": name, "compose_file": source}, 204)
	created := c.object("POST", "/api/v1/stacks", map[string]string{"name": name, "compose_file": source}, 201)
	id := created["id"].(string)
	stackPath := "/api/v1/stacks/" + id
	t.Cleanup(func() { c.cleanupStack(id) })
	c.waitStackState(id, "Idle")
	started := c.mutate(http.MethodPost, stackPath+"/start", nil, "application/json")
	c.waitOperation(started.OperationID)
	c.waitStackState(id, "RunningPrimary")
	execCommand := func(command string) string {
		t.Helper()
		containers := c.expect("GET", stackPath+"/containers", nil, 200).([]interface{})
		if len(containers) != 1 {
			t.Fatalf("unexpected containers %+v", containers)
		}
		containerID := containers[0].(map[string]interface{})["id"].(string)
		result := c.object("POST", stackPath+"/containers/"+containerID+"/exec", map[string]interface{}{"command": []string{"sh", "-c", command}}, 200)
		if result["exit_code"] != float64(0) {
			t.Fatalf("exec %q: %+v", command, result)
		}
		stdout, _ := result["stdout"].(string)
		return stdout
	}
	t.Log("verify data across container and stack lifecycle mutations")
	execCommand("printf persistent-marker > /data/persistent.txt")
	assertData := func(want string) {
		t.Helper()
		if got := execCommand("cat /data/persistent.txt"); got != want {
			t.Fatalf("persistent data: %q want %q", got, want)
		}
	}
	for _, action := range []string{"restart", "stop", "start"} {
		mutation := c.mutate("POST", stackPath+"/"+action, nil, "application/json")
		c.waitOperation(mutation.OperationID)
		if action == "stop" {
			c.waitStackState(id, "Idle")
		} else {
			c.waitStackState(id, "RunningPrimary")
			assertData("persistent-marker")
		}
	}
	containers := c.expect("GET", stackPath+"/containers", nil, 200).([]interface{})
	containerID := containers[0].(map[string]interface{})["id"].(string)
	c.expect("POST", stackPath+"/containers/"+containerID+"/stop", nil, 200)
	waitFor(t, 30*time.Second, func() error {
		items := c.expect("GET", stackPath+"/containers", nil, 200).([]interface{})
		if len(items) > 0 && items[0].(map[string]interface{})["status"] != "running" {
			return nil
		}
		return fmt.Errorf("container still running")
	})
	c.expect("POST", stackPath+"/containers/"+containerID+"/start", nil, 200)
	waitFor(t, 30*time.Second, func() error {
		items := c.expect("GET", stackPath+"/containers", nil, 200).([]interface{})
		if len(items) > 0 && items[0].(map[string]interface{})["status"] == "running" {
			return nil
		}
		return fmt.Errorf("container not running")
	})
	assertData("persistent-marker")
	t.Log("restore snapshot data and compose configuration")
	snapshots := c.mutate("POST", stackPath+"/snapshots", nil, "application/json")
	c.waitOperation(snapshots.OperationID)
	execCommand("printf changed-marker > /data/persistent.txt")
	updated := strings.Replace(source, "echo v1", "echo v2", 1)
	c.object("PUT", stackPath+"/compose", map[string]string{"compose_file": updated}, 200)
	c.waitStackState(id, "RunningPrimary")
	assertData("changed-marker")
	restored := c.mutate("POST", stackPath+"/snapshots/"+snapshots.Snapshot+"/restore", nil, "application/json")
	c.waitOperation(restored.OperationID)
	c.waitStackState(id, "RunningPrimary")
	assertData("persistent-marker")
	configResponse, payload, err := c.request("GET", stackPath+"/config", c.token, "", "", nil)
	if err != nil || configResponse.StatusCode != 200 || strings.Contains(string(payload), "echo v2") {
		t.Fatalf("snapshot did not restore compose: %s %v", payload, err)
	}
	t.Log("verify successful upgrade and failed-health upgrade rollback")
	upgraded := c.mutateJSON("POST", stackPath+"/upgrade", map[string]interface{}{"images": map[string]string{"app": "alpine:3.20"}})
	c.waitOperation(upgraded.OperationID)
	c.waitStackState(id, "RunningPrimary")
	assertData("persistent-marker")
	failed := c.mutateJSON("POST", stackPath+"/upgrade", map[string]interface{}{"images": map[string]string{"app": "alpine:3.21"}})
	waitFor(t, 3*time.Minute, func() error {
		op := c.object("GET", "/api/v1/operations/"+failed.OperationID, nil, 200)
		if op["state"] == "failed" {
			if op["checkpoint"] != "rolled-back" {
				t.Fatalf("upgrade failed without rollback: %+v", op)
			}
			return nil
		}
		if op["state"] == "succeeded" {
			t.Fatal("unhealthy image upgrade succeeded")
		}
		return fmt.Errorf("upgrade state: %v", op["state"])
	})
	c.waitStackState(id, "RunningPrimary")
	assertData("persistent-marker")
	c.expect("POST", stackPath+"/failover", nil, 409)
	c.expect("POST", stackPath+"/restore", nil, 409)
	c.expect("POST", "/api/v1/failover/"+id+"/trigger", nil, 409)
	c.expect("POST", "/api/v1/failover/"+id+"/abort", nil, 409)
	c.expect("GET", "/api/v1/failover/"+id+"/log", nil, 404)
	c.expect("POST", "/api/v1/storage/replication/"+id+"/trigger", nil, 400)
	c.waitStackState(id, "RunningPrimary")
	assertData("persistent-marker")
	c.assertPrefixAllocation(name, id)
	alerts := c.object("GET", stackPath+"/alerts", nil, 200)
	rows := alerts["rows"].([]interface{})
	if len(rows) == 0 {
		t.Fatal("compose alert rule not registered")
	}
	ruleID := rows[0].([]interface{})[0].(string)
	c.expect("POST", "/api/v1/internal/alerts/transition", map[string]interface{}{"rule_id": ruleID, "state": "firing", "observed": 99, "observed_at": time.Now().UTC().Format(time.RFC3339)}, 204)
	events := c.object("GET", stackPath+"/events?limit=200", nil, 200)
	if len(events["items"].([]interface{})) == 0 {
		t.Fatal("no persisted lifecycle events")
	}
	t.Log("verify durable metadata and workload data after service restarts")
	c.assertServiceRestarts(id, assertData)
	c.expect("GET", "/api/v1/storage/datasets", nil, 200)
	t.Log("verify stopped-dataset rollback and destroy_newer behavior")
	c.assertDatasetRollback(id)
	// Retained deletion must leave the managed dataset and its data on disk.
	dataset := c.object("GET", "/api/v1/storage/datasets/"+id, nil, 200)["name"].(string)
	deleted := c.mutate("DELETE", stackPath, nil, "")
	c.waitOperation(deleted.OperationID)
	c.waitStackDeleted(id)
	root, _ := os.Getwd()
	out, err := guestCommand(root, "zfs list -H -o name '"+dataset+"'")
	if err != nil || strings.TrimSpace(out) != dataset {
		t.Fatalf("retained dataset missing: %s %v", out, err)
	}
	t.Cleanup(func() {
		out, err := guestCommand(root, "zfs list -H -o name -r floatlab | while IFS= read -r ds; do case \"$ds\" in '"+dataset+"'-recovery-*|'"+dataset+"'-restore-*) zfs destroy -r \"$ds\" ;; esac; done; zfs destroy -r '"+dataset+"'")
		if err != nil {
			t.Errorf("cleanup retained dataset: %s %v", out, err)
		}
	})
}

func (c *apiClient) assertPrefixAllocation(name, stackID string) {
	pool := c.object("POST", "/api/v1/network/pools", map[string]string{"name": name, "prefix": "fdfe:200::/127", "stack_id": stackID}, 201)
	pid := pool["id"].(string)
	c.t.Cleanup(func() {
		c.cleanupSQL("DELETE FROM ip_reservations WHERE prefix_pool=?", pid)
		c.cleanupSQL("DELETE FROM prefix_pools WHERE id=?", pid)
	})
	c.expect("GET", "/api/v1/network/pools", nil, 200)
	allocations := []string{}
	for range 2 {
		allocation := c.object("POST", "/api/v1/network/allocations", map[string]string{"stack_id": stackID, "service": "app", "prefix_pool": pid}, 201)
		allocations = append(allocations, allocation["id"].(string))
	}
	c.expect("POST", "/api/v1/network/allocations", map[string]string{"stack_id": stackID, "service": "app", "prefix_pool": pid}, 409)
	for _, id := range allocations {
		c.expect("DELETE", "/api/v1/network/allocations/"+id, nil, 204)
	}
}

func (c *apiClient) assertServiceRestarts(id string, assertData func(string)) {
	t := c.t
	root, _ := os.Getwd()
	for _, command := range []string{
		`docker restart $(docker ps -q --filter label=com.docker.compose.service=floatlab-control --filter label=com.docker.compose.project=floatlab-system)`,
		`systemctl restart floatlab-hostd.service`,
	} {
		out, err := guestCommand(root, command)
		if err != nil {
			t.Fatalf("restart service: %s %v", out, err)
		}
		c.waitReady()
		c.waitNodeReady()
		c.waitStackState(id, "RunningPrimary")
		assertData("persistent-marker")
		mutation := c.mutate("POST", "/api/v1/stacks/"+id+"/restart", nil, "application/json")
		c.waitOperation(mutation.OperationID)
		c.waitStackState(id, "RunningPrimary")
		assertData("persistent-marker")
	}
}

func (c *apiClient) assertDatasetRollback(id string) {
	t := c.t
	root, _ := os.Getwd()
	stackPath := "/api/v1/stacks/" + id
	dataset := c.object("GET", "/api/v1/storage/datasets/"+id, nil, 200)
	mount := dataset["mount_point"].(string)
	stopped := c.mutate("POST", stackPath+"/stop", nil, "application/json")
	c.waitOperation(stopped.OperationID)
	c.waitStackState(id, "Idle")
	writeMarker := func(marker string) {
		t.Helper()
		out, err := guestCommand(root, "printf '"+marker+"' > '"+mount+"/rollback-marker'")
		if err != nil {
			t.Fatalf("write dataset marker: %s %v", out, err)
		}
	}
	createSnapshot := func(label string) string {
		t.Helper()
		created := c.mutateJSON("POST", "/api/v1/storage/datasets/"+id+"/snapshots", map[string]string{"label": label})
		c.waitOperation(created.OperationID)
		return created.Snapshot
	}
	writeMarker("before")
	older := createSnapshot("older")
	writeMarker("after")
	newer := createSnapshot("newer")
	path := "/api/v1/storage/datasets/" + id + "/snapshots/" + older + "/restore"
	c.expect("POST", path, map[string]bool{"destroy_newer": false}, 409)
	restore := c.mutateJSON("POST", path, map[string]bool{"destroy_newer": true})
	c.waitOperation(restore.OperationID)
	out, err := guestCommand(root, "cat '"+mount+"/rollback-marker'")
	if err != nil || out != "before" {
		t.Fatalf("rollback data: %q %v", out, err)
	}
	snapshots := c.expect("GET", "/api/v1/storage/datasets/"+id+"/snapshots", nil, 200).([]interface{})
	for _, snapshot := range snapshots {
		if snapshot.(map[string]interface{})["name"] == newer {
			t.Fatal("newer snapshot survived destroy_newer")
		}
	}
}
