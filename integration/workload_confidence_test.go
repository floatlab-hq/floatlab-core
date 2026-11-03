package integration_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/api/openapi"
)

func (c *apiClient) assertUnconfiguredLab() {
	c.t.Helper()
	operations, err := openapi.ManagementOperations()
	if err != nil {
		c.t.Fatal(err)
	}
	for _, operation := range operations {
		if !strings.HasPrefix(operation.Path, "/api/v1/lab/") {
			continue
		}
		path := strings.NewReplacer("{id}", "unconfigured-node", "{node_id}", "unconfigured-node").Replace(operation.Path)
		c.expect(operation.Method, path, map[string]any{}, http.StatusServiceUnavailable)
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
func requireFields(t testT, row map[string]interface{}, fields ...string) {
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
	rule := c.object("POST", "/api/v1/alert-rules", map[string]interface{}{"name": name, "condition": "up == 0", "recovery_condition": "up == 1", "freshness_query": "timestamp(up)", "severity": "critical", "node_id": id}, 201)
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
	// Successful received-backup reads/restores require the separate multi-node suite.
	c.expect("GET", "/api/v1/storage/backups?node_id="+c.nodeID+"&source_node_id=invalid/source&dataset=invalid", nil, 400)
	c.expect("POST", "/api/v1/storage/backups/restore", map[string]any{}, 400)
	c.expect("GET", "/api/v1/storage/faults", nil, 200)
	c.expect("GET", "/api/v1/failover/status", nil, 200)
}

func (c *apiClient) cleanupSQL(statement string, id string) {
	c.t.Helper()
	binding := `.parameter set ?1 "CAST(X'` + hex.EncodeToString([]byte(id)) + `' AS TEXT)"`
	out, err := guestCommand(c.vm, "sqlite3 -batch -bail -cmd "+shellQuote(".timeout 10000")+" -cmd "+shellQuote(binding)+" /floatlab/system/workloads/local.sqlite "+shellQuote(statement))

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
	if c.coverage != nil {
		c.coverage.observe("GET", "/api/v1/events", 200)
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
