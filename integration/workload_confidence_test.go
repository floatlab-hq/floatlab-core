package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/floatlab/floatlab-core/api/openapi"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestWorkloadConfidence(t *testing.T) {
	if os.Getenv("FLOATLAB_WORKLOAD_INTEGRATION") != "1" {
		t.Skip("run scripts/test-workloads.sh for the complete single-node suite")
	}
	preflightWorkloads(t)
	// Never recreate an existing user-selected domain; this suite owns a unique VM.
	if os.Getenv("API_URL") == "" {
		t.Setenv("VM_NAME", fmt.Sprintf("floatlab-confidence-%d", time.Now().UnixNano()))
	}
	coverage := newCoverage(t)
	activeCoverage = coverage
	t.Cleanup(func() { activeCoverage = nil })
	t.Cleanup(func() { coverage.report(t) })
	c := newAPIClient(t, startAppliance(t))
	c.waitReady()
	c.login()
	c.waitNodeReady()
	t.Setenv("API_URL", c.baseURL)
	t.Setenv("FLOATLAB_VM_INTEGRATION", "1")
	t.Setenv("FLOATLAB_CLI_INTEGRATION", "1")
	diagnostics := func(target *testing.T) {
		if target.Failed() {
			root, _ := os.Getwd()
			out, err := guestCommand(root, `docker ps -a; docker compose -f /floatlab/system/docker-compose.yml logs --tail=100 floatlab-control; journalctl -u floatlab-hostd --no-pager -n 100; zfs list -t all; docker ps -aq --filter label=com.docker.compose.project | while read -r container; do docker inspect --format '{{.Name}} {{json .State}}' "$container"; docker logs --tail=40 "$container" 2>&1; done; rqlite_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $(docker ps -q --filter label=com.docker.compose.service=rqlite --filter label=com.docker.compose.project=floatlab-system)); curl -fsS -H 'Content-Type: application/json' --data '["SELECT id,state,checkpoint,error FROM operations ORDER BY created_at DESC LIMIT 20","SELECT * FROM lifecycle_events ORDER BY occurred_at DESC LIMIT 30"]' "http://$rqlite_ip:4001/db/query"`)
			target.Logf("appliance diagnostics: %s\n%v", out, err)
		}
	}
	t.Cleanup(func() { diagnostics(t) })
	for _, scenario := range []struct {
		name string
		fn   func(*testing.T)
	}{
		{"container-management", TestContainerManagementAPI},
		{"cli-caddy", TestCLICaddy},
		{"documented-management", func(t *testing.T) { c := newAPIClient(t, c.baseURL); c.login(); c.assertManagementContracts() }},
		{"persistence-recovery-rollback", func(t *testing.T) { c := newAPIClient(t, c.baseURL); c.login(); c.assertRecoveryWorkload() }},
	} {
		if !coverage.scenarioTest(t, scenario.name, func(child *testing.T) {
			defer diagnostics(child) // Capture workloads before owned-resource cleanup runs.
			scenario.fn(child)
		}) {
			return
		}
	}
}

func preflightWorkloads(t *testing.T) {
	t.Helper()
	for _, name := range []string{"go", "bun"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("full suite requires %s on PATH", name)
		}
	}
	if os.Getenv("API_URL") != "" {
		if os.Getenv("VM_SSH") != "" {
			if _, err := exec.LookPath("ssh"); err != nil {
				t.Fatal("existing appliance mode requires ssh on PATH")
			}
		} else if os.Getenv("VM_NAME") != "" {
			if _, err := exec.LookPath("virsh"); err != nil {
				t.Fatal("guest-agent appliance mode requires virsh on PATH")
			}
		} else {
			t.Fatal("existing appliance mode requires VM_SSH or VM_NAME for a dedicated test appliance")
		}
		return
	}
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Fatal("full suite requires /dev/kvm, or API_URL and VM_SSH for a dedicated appliance")
	}
	for _, name := range []string{"nix", "virsh", "virt-install"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatalf("full suite requires %s", name)
		}
	}
}

func (c *apiClient) expect(method, path string, body interface{}, status int) interface{} {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(mustJSON(c.t, body))
	}
	response, payload, err := c.request(method, path, c.token, fmt.Sprintf("confidence-%d", time.Now().UnixNano()), "application/json", reader)
	if err != nil {
		c.t.Fatal(err)
	}
	if response.StatusCode != status {
		c.t.Fatalf("%s %s: %s %s; want %d", method, path, response.Status, payload, status)
	}
	if status >= 200 && status < 300 {
		operations, err := openapi.ManagementOperations()
		if err != nil {
			c.t.Fatal(err)
		}
		for _, op := range operations {
			if op.Method == method && matchesPath(op.Path, strings.SplitN(path, "?", 2)[0]) {
				if err := openapi.ValidateJSONResponse(method, op.Path, status, payload); err != nil {
					c.t.Fatalf("%s %s: %v", method, path, err)
				}
				break
			}
		}
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(payload, &value); err != nil {
		c.t.Fatalf("%s %s: invalid JSON %s", method, path, payload)
	}
	return value
}
func (c *apiClient) object(method, path string, body interface{}, status int) map[string]interface{} {
	c.t.Helper()
	value := c.expect(method, path, body, status)
	object, ok := value.(map[string]interface{})
	if !ok {
		c.t.Fatalf("%s %s: expected object, got %T", method, path, value)
	}
	return object
}
func requireFields(t *testing.T, row map[string]interface{}, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if _, ok := row[field]; !ok {
			t.Fatalf("missing field %s: %+v", field, row)
		}
	}
}

func (c *apiClient) assertManagementContracts() {
	t := c.t
	name := fmt.Sprintf("contracts-%d", time.Now().UnixNano())
	operations, err := openapi.ManagementOperations()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range operations {
		if op.Public {
			continue
		}
		response, payload, err := c.request(op.Method, op.Path, "", "", "application/json", strings.NewReader(`{}`))
		if err != nil || response.StatusCode != 401 {
			t.Fatalf("unauthorized %s %s: %+v %s %v", op.Method, op.Path, response, payload, err)
		}
	}
	health := c.object("GET", "/api/v1/health", nil, 200)
	requireFields(t, health, "status", "version", "uptime_seconds")
	ready := c.object("GET", "/api/v1/health/ready", nil, 200)
	if ready["ready"] != true {
		t.Fatalf("not ready: %+v", ready)
	}
	raft := c.object("GET", "/api/v1/health/raft", nil, 200)
	requireFields(t, raft, "leader", "state", "peers", "commit_index", "last_log_index")
	node := c.object("POST", "/api/v1/nodes", map[string]interface{}{"name": name, "hostname": "unreachable.invalid", "zfs_pool": "floatlab"}, 201)
	id := node["id"].(string)
	t.Cleanup(func() { c.expect("DELETE", "/api/v1/nodes/"+id, nil, 204) })
	requireFields(t, node, "hostname", "status", "role", "zfs_pool", "addresses", "created_at")
	node = c.object("PUT", "/api/v1/nodes/"+id, map[string]string{"name": name + "-updated"}, 200)
	if node["name"] != name+"-updated" {
		t.Fatal("node update not persisted")
	}
	c.object("GET", "/api/v1/nodes/"+id, nil, 200)
	c.expect("GET", "/api/v1/nodes/"+id+"/stacks", nil, 200)
	nodeHealth := c.object("GET", "/api/v1/nodes/"+id+"/health", nil, 200)
	if nodeHealth["status"] != "unreachable" {
		t.Fatalf("offline node health %+v", nodeHealth)
	}
	c.expect("GET", "/api/v1/nodes/missing", nil, 404)
	network := c.object("POST", "/api/v1/networks", map[string]string{"name": name, "cidr": "fdfe:100::/64"}, 201)
	nid := network["id"].(string)
	t.Cleanup(func() { c.expect("DELETE", "/api/v1/networks/"+nid, nil, 204) })
	c.expect("GET", "/api/v1/networks", nil, 200)
	c.object("GET", "/api/v1/networks/"+nid, nil, 200)
	c.expect("POST", "/api/v1/networks", map[string]string{"name": name + "-overlap", "cidr": "fdfe:100::/65"}, 409)
	rule := c.object("POST", "/api/v1/alert-rules", map[string]interface{}{"name": name, "condition": "up == 0", "severity": "critical", "node_id": id}, 201)
	rid := rule["id"].(string)
	t.Cleanup(func() { c.expect("DELETE", "/api/v1/alert-rules/"+rid, nil, 204) })
	updated := c.object("PUT", "/api/v1/alert-rules/"+rid, map[string]interface{}{"enabled": false}, 200)
	if updated["enabled"] != false {
		t.Fatal("rule update lost")
	}
	c.object("GET", "/api/v1/alert-rules/"+rid, nil, 200)
	rules := c.expect("GET", "/api/v1/alert-rules?node_id="+id+"&enabled=false", nil, 200).([]interface{})
	if len(rules) != 1 {
		t.Fatalf("rule filtering: %+v", rules)
	}
	c.expect("GET", "/api/v1/alerts", nil, 200)
	// Drive real alert ingestion and read the persisted alert, rather than seed SQL.
	c.expect("POST", "/api/v1/stats/webhook", map[string]interface{}{"alerts": []interface{}{map[string]interface{}{"status": "firing", "labels": map[string]string{"alertname": name, "rule_id": rid, "severity": "critical", "node_id": id}, "annotations": map[string]string{"summary": name, "description": "contract alert"}, "startsAt": time.Now().UTC().Format(time.RFC3339)}}}, 200)
	alerts := c.expect("GET", "/api/v1/alerts?node_id="+id+"&severity=critical", nil, 200).([]interface{})
	if len(alerts) == 0 {
		t.Fatal("webhook did not persist alert")
	}
	aid := alerts[0].(map[string]interface{})["id"].(string)
	c.object("GET", "/api/v1/alerts/"+aid, nil, 200)
	t.Cleanup(func() {
		c.cleanupSQL("DELETE FROM alerts WHERE id=?", aid)
		c.cleanupSQL("DELETE FROM notifications WHERE title=?", name)
	})
	c.assertNotificationContract(name)
	c.expect("GET", "/api/v1/storage/replication", nil, 200)
	c.expect("GET", "/api/v1/storage/faults", nil, 200)
	c.expect("GET", "/api/v1/failover/status", nil, 200)
}

func (c *apiClient) cleanupSQL(statement string, id string) {
	c.t.Helper()
	payload := mustJSON(c.t, [][]interface{}{{statement, id}})
	root, _ := os.Getwd()
	out, err := guestCommand(root, `rqlite_ip=$(docker inspect --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $(docker ps -q --filter label=com.docker.compose.service=rqlite --filter label=com.docker.compose.project=floatlab-system)); curl -fsS -H 'Content-Type: application/json' --data '`+strings.ReplaceAll(string(payload), "'", "'\\''")+`' "http://$rqlite_ip:4001/db/execute?transaction"`)

	if err != nil || strings.Contains(out, `"error"`) {
		c.t.Errorf("cleanup owned record: %s %v", out, err)
	}
}

func (c *apiClient) assertNotificationContract(name string) {
	t := c.t
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	streams := make([]*http.Response, 0, 2)
	for range 2 {
		request, _ := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/v1/events", nil)
		request.Header.Set("Authorization", "Bearer "+c.token)
		response, err := c.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
			response.Body.Close()
			t.Fatalf("SSE: %s", response.Status)
		}
		streams = append(streams, response)
		defer response.Body.Close()
	}
	n := c.object("POST", "/api/v1/notifications", map[string]string{"title": name, "body": "SSE contract", "severity": "info", "stack_id": name}, 201)
	id := n["id"].(string)
	t.Cleanup(func() { c.expect("DELETE", "/api/v1/notifications/"+id, nil, 204) })
	for _, stream := range streams {
		buffer := make([]byte, 4096)
		observed := ""
		for !strings.Contains(observed, id) {
			n, err := stream.Body.Read(buffer)
			observed += string(buffer[:n])
			if err != nil {
				t.Fatalf("SSE missing notification: %s %v", observed, err)
			}
		}
		if !strings.Contains(observed, "notification.new") {
			t.Fatal("SSE missing event type")
		}
	}
	if activeCoverage != nil {
		activeCoverage.observe("GET", "/api/v1/events", 200)
	}
	n = c.object("GET", "/api/v1/notifications/"+id, nil, 200)
	if n["state"] != "unread" {
		t.Fatal("new notification not unread")
	}
	n = c.object("POST", "/api/v1/notifications/"+id+"/read", nil, 200)
	if n["state"] != "read" {
		t.Fatal("read did not persist")
	}
	c.expect("POST", "/api/v1/notifications/"+id+"/silence", map[string]string{"until": "invalid"}, 400)
	n = c.object("POST", "/api/v1/notifications/"+id+"/silence", map[string]string{"until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, 200)
	if n["state"] != "silenced" {
		t.Fatal("silence did not persist")
	}
	n = c.object("POST", "/api/v1/notifications/"+id+"/resolve", nil, 200)
	if n["state"] != "resolved" || n["resolved_at"] == nil {
		t.Fatal("resolve did not persist")
	}
	rows := c.expect("GET", "/api/v1/notifications?state=resolved&stack_id="+name+"&limit=1", nil, 200).([]interface{})
	if len(rows) != 1 || rows[0].(map[string]interface{})["id"] != id {
		t.Fatalf("notification filtering: %+v", rows)
	}
	c.expect("GET", "/api/v1/notifications/missing", nil, 404)
}
