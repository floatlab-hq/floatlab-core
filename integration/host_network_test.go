package integration_test

import (
	"fmt"
	"time"
)

func (c *apiClient) assertHostNetworkTransactions() {
	t := c.t
	nodes := c.expect("GET", "/api/v1/nodes", nil, 200).([]interface{})
	if len(nodes) != 1 {
		t.Fatalf("expected one dedicated appliance node, got %d", len(nodes))
	}
	path := "/api/v1/nodes/" + nodes[0].(map[string]interface{})["id"].(string) + "/settings/network"
	// Reapply the current settings: exercise real confirmation/recovery without
	// replacing the test appliance's reachable address or DNS configuration.
	for _, action := range []string{"confirm", "rollback"} {
		status := c.object("GET", path, nil, 200)
		change := c.object("PUT", path, map[string]interface{}{"revision": status["revision"], "config": status["config"]}, 202)
		changePath := path + "/changes/" + change["id"].(string)
		waitFor(t, 45*time.Second, func() error {
			change = c.object("GET", changePath, nil, 200)
			if change["state"] == "failed" || change["state"] == "rolled_back" {
				t.Fatalf("network transaction failed before %s: %+v", action, change)
			}
			if change["state"] != "awaiting_confirmation" {
				return fmt.Errorf("network change is %s", change["state"])
			}
			return nil
		})
		change = c.object("POST", changePath+"/"+action, nil, 200)
		want := "confirmed"
		if action == "rollback" {
			want = "rolled_back"
		}
		if change["state"] != want {
			t.Fatalf("network %s returned %+v", action, change)
		}
		after := c.object("GET", path, nil, 200)
		if after["revision"] != status["revision"] {
			t.Fatal("reapplying identical network settings changed their revision")
		}
	}
}
