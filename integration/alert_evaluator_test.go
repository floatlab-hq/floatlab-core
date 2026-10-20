package integration_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAlertEvaluator(t *testing.T) {
	if os.Getenv("FLOATLAB_VM_INTEGRATION") != "1" {
		t.Skip("run with FLOATLAB_VM_INTEGRATION=1 against a dedicated test appliance")
	}
	c := newAPIClient(t, startAppliance(t))
	c.waitReady()
	c.login()
	c.waitNodeReady()
	root, _ := os.Getwd()
	name := fmt.Sprintf("alerts-it-%d", time.Now().UnixNano())
	metric := `fl_alert_evaluator_test{test_case=` + strconv.Quote(name) + `}`
	rule := c.object("POST", "/api/v1/alert-rules", map[string]interface{}{
		"name": name, "condition": metric + " > 90", "recovery_condition": metric + " < 85",
		"freshness_query": "timestamp(" + metric + ")", "severity": "warning",
		"for_duration": "30s", "recovery_duration": "30s", "max_sample_age": "45s", "node_id": "node1",
	}, 201)
	ruleID := rule["id"].(string)
	t.Cleanup(func() {
		c.expect("DELETE", "/api/v1/alert-rules/"+ruleID, nil, 204)
		c.cleanupSQL("DELETE FROM notifications WHERE alert_id IN (SELECT id FROM alerts WHERE rule_id=?)", ruleID)
		c.cleanupSQL("DELETE FROM alerts WHERE rule_id=?", ruleID)
		c.cleanupSQL("DELETE FROM alert_evaluations WHERE rule_id=?", ruleID)
	})
	t.Cleanup(func() {
		if t.Failed() {
			out, err := guestCommand(root, `docker logs --tail=80 $(docker ps -q --filter label=com.docker.compose.service=floatlab-control --filter label=com.docker.compose.project=floatlab-system)`)
			t.Logf("evaluator diagnostics: %s\n%v", out, err)
		}
	})
	push := func(a, b int) {
		t.Helper()
		body := fmt.Sprintf("fl_alert_evaluator_test{test_case=%q,node_id=\"node1\",device=\"a\"} %d\nfl_alert_evaluator_test{test_case=%q,node_id=\"node1\",device=\"b\"} %d\n", name, a, name, b)
		out, err := guestCommand(root, "curl -fsS --data-binary '"+body+"' http://127.0.0.1:8428/api/v1/import/prometheus")
		if err != nil {
			t.Fatalf("push evaluator samples: %s %v", out, err)
		}
	}
	incidents := func() []map[string]interface{} {
		t.Helper()
		var own []map[string]interface{}
		for _, value := range c.expect("GET", "/api/v1/alerts?node_id=node1", nil, 200).([]interface{}) {
			row := value.(map[string]interface{})
			if row["rule_id"] == ruleID {
				own = append(own, row)
			}
		}
		return own
	}
	active := func() map[string]string {
		result := map[string]string{}
		for _, row := range incidents() {
			if row["state"] == "active" {
				result[row["labels"].(map[string]interface{})["device"].(string)] = row["id"].(string)
			}
		}
		return result
	}
	firingNotifications := func() int {
		t.Helper()
		ids := map[string]bool{}
		for _, row := range incidents() {
			ids[row["id"].(string)] = true
		}
		count := 0
		for _, value := range c.expect("GET", "/api/v1/notifications", nil, 200).([]interface{}) {
			row := value.(map[string]interface{})
			if id, ok := row["alert_id"].(string); ok && ids[id] && row["kind"] == "alert" {
				count++
			}
		}
		return count
	}
	t.Log("waiting for sustained firing on two independent resources")
	waitFor(t, 3*time.Minute, func() error {
		push(95, 95)
		if len(active()) != 2 {
			return fmt.Errorf("waiting for two active incidents")
		}
		return nil
	})
	first := active()
	if len(incidents()) != 2 || firingNotifications() != 2 {
		t.Fatal("expected two incidents and exactly one firing notification each")
	}

	t.Log("restarting management and checking persistent identity and deduplication")
	out, err := guestCommand(root, `docker restart $(docker ps -q --filter label=com.docker.compose.service=floatlab-control --filter label=com.docker.compose.project=floatlab-system)`)
	if err != nil {
		t.Fatalf("restart management: %s %v", out, err)
	}
	c.waitReady()
	c.login()
	resumed := time.Now().UTC()
	waitFor(t, 2*time.Minute, func() error {
		push(95, 95)
		status := c.object("GET", "/api/v1/alert-rules/"+ruleID, nil, 200)
		for _, value := range status["evaluations"].([]interface{}) {
			row := value.(map[string]interface{})
			at, _ := time.Parse(time.RFC3339Nano, row["evaluated_at"].(string))
			if !at.After(resumed) {
				return fmt.Errorf("waiting for evaluation after restart")
			}
		}
		return nil
	})
	if current := active(); len(current) != 2 || current["a"] != first["a"] || current["b"] != first["b"] || firingNotifications() != 2 {
		t.Fatalf("restart changed incidents or duplicated notifications: %+v", current)
	}

	t.Log("withholding samples: stale telemetry must not resolve incidents")
	waitFor(t, 2*time.Minute, func() error {
		for _, row := range incidents() {
			if row["state"] != "active" {
				return fmt.Errorf("stale telemetry resolved incident: %+v", row)
			}
			if row["evaluation_state"] != "unknown" {
				return fmt.Errorf("waiting for unknown evaluation status")
			}
		}
		return nil
	})
	if len(active()) != 2 || firingNotifications() != 2 {
		t.Fatal("stale data changed incidents or notifications")
	}

	t.Log("recovering one resource while the other remains firing")
	waitFor(t, 3*time.Minute, func() error {
		push(80, 95)
		current := active()
		if len(current) != 1 || current["b"] != first["b"] {
			return fmt.Errorf("waiting for independent recovery: %+v", current)
		}
		return nil
	})
	t.Log("repeating the condition: recurrence must retain resolved history")
	waitFor(t, 3*time.Minute, func() error {
		push(95, 95)
		current := active()
		if len(current) != 2 || current["a"] == first["a"] || current["b"] != first["b"] {
			return fmt.Errorf("waiting for a new incident occurrence: %+v", current)
		}
		return nil
	})
	if len(incidents()) != 3 || firingNotifications() != 3 {
		t.Fatal("recurrence lost history or duplicated firing notifications")
	}
	c.object("PUT", "/api/v1/alert-rules/"+ruleID, map[string]bool{"enabled": false}, 200)
	waitFor(t, time.Minute, func() error {
		for _, row := range incidents() {
			if row["state"] != "resolved" {
				return fmt.Errorf("waiting for disabled rule closure")
			}
			if row["id"] != first["a"] && !strings.Contains(row["resolution_reason"].(string), "disabled") {
				return fmt.Errorf("missing closure reason: %+v", row)
			}
		}
		return nil
	})
}
