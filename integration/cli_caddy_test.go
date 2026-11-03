package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
)

func runCLICaddy(t testT, c *apiClient) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Fatal("Bun and installed workspace dependencies are required to build the CLI")
	}
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "floatlab")
	run(t, temporary, nil, "bun", "build", "--compile", filepath.Join(root, "cli/src/main.ts"), "--outfile", binary)
	c.waitReady()
	c.login()
	c.waitNodeReady()
	var nodes []struct {
		ID string `json:"id"`
	}
	if err := c.doJSON("GET", "/api/v1/nodes", c.token, "", nil, &nodes); err != nil || len(nodes) == 0 {
		t.Fatalf("nodes=%v error=%v", nodes, err)
	}
	cli := func(args ...string) (string, string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "FLOATLAB_URL="+c.baseURL, "FLOATLAB_TOKEN="+c.token, "XDG_CONFIG_HOME="+temporary)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatalf("CLI %v: %v", args, err)
			}
		}
		return stdout.String(), stderr.String(), code
	}
	mustCLI := func(args ...string) string {
		t.Helper()
		stdout, stderr, code := cli(args...)
		if code != 0 {
			t.Fatalf("CLI %v exited %d\nstdout: %s\nstderr: %s", args, code, stdout, stderr)
		}
		return stdout
	}
	name := fmt.Sprintf("cli-it-%d", time.Now().UnixNano())
	marker := "floatlab-caddy-" + name
	poolName := name + "-pool"
	var pools []struct {
		CIDR string `json:"cidr"`
	}
	if err := c.doJSON("GET", "/api/v1/settings/network-pools", c.token, "", nil, &pools); err != nil {
		t.Fatal(err)
	}
	subnet := ""
	for octet := 240; octet < 255; octet++ {
		candidate := fmt.Sprintf("10.254.%d.0/24", octet)
		prefix := netip.MustParsePrefix(candidate)
		overlap := false
		for _, pool := range pools {
			existing, err := netip.ParsePrefix(pool.CIDR)
			if err == nil && (prefix.Contains(existing.Addr()) || existing.Contains(prefix.Addr())) {
				overlap = true
			}
		}
		if !overlap {
			subnet = candidate
			break
		}
	}
	if subnet == "" {
		t.Fatal("no free test subnet in 10.254.240.0/20")
	}
	prefix := netip.MustParsePrefix(subnet)
	startIP := prefix.Addr().Next().Next()
	endIP := startIP.Next().Next()
	_, err = guestCommand(c.vm, "ip link show dev floatlab-lan")
	if err != nil {
		t.Fatalf("installer did not configure the LAN bridge: %v", err)
	}
	// Pools must belong to a node and match one of its actual LAN subnets.
	lanAddress := prefix.Addr().Next().String() + "/24"
	if out, err := guestCommand(c.vm, "ip address add "+lanAddress+" dev floatlab-lan"); err != nil {
		t.Fatalf("add test LAN address: %s %v", out, err)
	}
	t.Cleanup(func() {
		if _, err := guestCommand(c.vm, "ip address del "+lanAddress+" dev floatlab-lan"); err != nil {
			t.Errorf("remove test LAN address: %v", err)
		}
	})
	var pool struct {
		ID string `json:"id"`
	}
	poolBody := mustJSON(t, map[string]any{"name": poolName, "cidr": subnet, "start_ip": startIP.String(), "end_ip": endIP.String(), "node_ids": []string{nodes[0].ID}})
	if err := c.doJSON("POST", "/api/v1/settings/network-pools", c.token, "application/json", bytes.NewReader(poolBody), &pool, name+"-pool-create"); err != nil {
		t.Fatal(err)
	}
	if err := c.doJSON("PUT", "/api/v1/settings/network-pools/"+pool.ID, c.token, "application/json", bytes.NewReader(poolBody), &pool, name+"-pool-update"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pool.ID == "" {
			return
		}
		if err := c.doJSON("DELETE", "/api/v1/settings/network-pools/"+pool.ID, c.token, "", nil, nil, name+"-pool-cleanup"); err != nil {
			t.Errorf("cleanup pool: %v", err)
		}
	})
	stackID := ""
	t.Cleanup(func() {
		if stackID == "" {
			var stacks []struct{ ID, Name string }
			if c.doJSON("GET", "/api/v1/stacks", c.token, "", nil, &stacks) == nil {
				for _, stack := range stacks {
					if stack.Name == name {
						stackID = stack.ID
					}
				}
			}
		}
		c.cleanupStack(stackID)
	})
	source := caddyCompose(name, poolName, nodes[0].ID, marker)
	file := filepath.Join(temporary, "compose.yaml")
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("create and start using the CLI")
	mustCLI("create", name, "--file", file, "--start")
	var stacks []struct{ ID, Name string }
	if err := c.doJSON("GET", "/api/v1/stacks", c.token, "", nil, &stacks); err != nil {
		t.Fatal(err)
	}
	for _, stack := range stacks {
		if stack.Name == name {
			stackID = stack.ID
		}
	}
	if stackID == "" {
		t.Fatal("CLI-created stack not found")
	}
	c.waitStackState(stackID, "RunningPrimary")
	if output := mustCLI("list"); !strings.Contains(output, name) || !strings.Contains(output, "RunningPrimary") {
		t.Fatalf("stack list: %s", output)
	}
	if output := mustCLI("list", name); !strings.Contains(output, "caddy") || !strings.Contains(output, "probe") {
		t.Fatalf("container list: %s", output)
	}
	if output := mustCLI("config", name); !strings.Contains(output, marker) || !strings.Contains(output, poolName) || !strings.Contains(output, "caddy:2-alpine") {
		t.Fatalf("saved config: %s", output)
	}
	var status struct {
		StackIP    string `json:"stack_ip"`
		Containers []struct {
			ID      string `json:"id"`
			Service string `json:"service"`
		} `json:"containers"`
	}
	if err := c.doJSON("GET", "/api/v1/stacks/"+stackID+"/status", c.token, "", nil, &status); err != nil {
		t.Fatal(err)
	}
	address, err := netip.ParseAddr(status.StackIP)
	if err != nil || !prefix.Contains(address) || address.Compare(startIP) < 0 || address.Compare(endIP) > 0 {
		t.Fatalf("allocated IP %q outside test pool", status.StackIP)
	}
	allocations := func() ([]struct {
		StackID string `json:"stack_id"`
		PoolID  string `json:"pool_id"`
		Address string `json:"address"`
		State   string `json:"state"`
	}, error) {
		var rows []struct {
			StackID string `json:"stack_id"`
			PoolID  string `json:"pool_id"`
			Address string `json:"address"`
			State   string `json:"state"`
		}
		err := c.doJSON("GET", "/api/v1/network/allocations?stack_id="+stackID, c.token, "", nil, &rows)
		return rows, err
	}
	allocated, err := allocations()
	if err != nil || len(allocated) != 1 || allocated[0].PoolID != pool.ID || allocated[0].State != "active" || allocated[0].Address != status.StackIP {
		t.Fatalf("allocations=%+v error=%v", allocated, err)
	}
	t.Log("verify service DNS, host access, and published port binding")
	if output := mustCLI("exec", name, "probe", "--", "wget", "-T", "5", "-qO-", "http://caddy/"+marker); output != marker {
		t.Fatalf("service DNS HTTP: %q", output)
	}
	hostOutput, err := guestCommand(c.vm, "curl --fail --silent --show-error http://"+status.StackIP+":8088/"+marker)
	if err != nil || hostOutput != marker {
		t.Fatalf("VM host HTTP: %q error=%v", hostOutput, err)
	}
	containerID := ""
	for _, container := range status.Containers {
		if container.Service == "caddy" {
			containerID = container.ID
		}
	}
	if containerID == "" {
		t.Fatal("Caddy container not found")
	}
	binding, err := guestCommand(c.vm, "docker inspect --format '{{json .NetworkSettings.Ports}}' "+containerID)
	if err != nil || !strings.Contains(binding, `"HostIp":"`+status.StackIP+`"`) || !strings.Contains(binding, `"HostPort":"8088"`) {
		t.Fatalf("published binding=%s error=%v", binding, err)
	}
	stdout, stderr, code := cli("exec", name, "caddy", "--", "sh", "-c", "printf '%s' \"$1\"; printf '%s' error-marker >&2; exit 7", "sh", "literal $HOME; a b")
	if code != 7 || stdout != "literal $HOME; a b" || stderr != "error-marker" {
		t.Fatalf("exec code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	t.Log("require ingested access logs and actual metric samples")
	waitFor(t, 90*time.Second, func() error {
		stdout, stderr, code := cli("logs", name, "caddy", "--limit", "100")
		if code != 0 || !strings.Contains(stdout, marker) {
			return fmt.Errorf("logs code=%d stdout=%s stderr=%s", code, stdout, stderr)
		}
		return nil
	})
	waitFor(t, 90*time.Second, func() error {
		var series []struct {
			Label  string `json:"label"`
			Points []struct {
				Value float64 `json:"value"`
			} `json:"points"`
		}
		if err := c.doJSON("GET", "/api/v1/stats/stacks/"+stackID+"?range=1h", c.token, "", nil, &series); err != nil {
			return err
		}
		for _, metric := range series {
			if metric.Label == "mem" {
				for _, point := range metric.Points {
					if point.Value > 0 {
						return nil
					}
				}
			}
		}
		return fmt.Errorf("no positive memory sample")
	})
	output := mustCLI("stats", name)
	if !strings.Contains(output, "mem") || !strings.Contains(output, "bytes") {
		t.Fatalf("stats output: %s", output)
	}
	{
		t.Log("verify HTTP and service DNS after independent control and host service restarts")
		c.assertServiceRestarts(stackID, func(_ string) {
			waitFor(t, 30*time.Second, func() error {
				output, err := guestCommand(c.vm, "curl --max-time 5 --fail --silent --show-error http://"+status.StackIP+":8088/"+marker)
				if err != nil || output != marker {
					return fmt.Errorf("HTTP after service restart: %q %v", output, err)
				}
				stdout, stderr, code := cli("exec", name, "probe", "--", "wget", "-T", "5", "-qO-", "http://caddy/"+marker)
				if code != 0 || stdout != marker {
					return fmt.Errorf("DNS after service restart: code=%d stdout=%q stderr=%q", code, stdout, stderr)
				}
				return nil
			})
		})
	}
	t.Log("delete through the CLI and verify networking cleanup")
	mustCLI("delete", name, "--purge")
	c.waitStackDeleted(stackID)
	allocated, err = allocations()
	if err != nil || len(allocated) != 0 {
		t.Fatalf("allocation leaked: %+v error=%v", allocated, err)
	}
	host := "flh" + strings.ReplaceAll(stackID, "-", "")[:10]
	cleanup, err := guestCommand(c.vm, "docker ps --all --quiet --filter label=com.docker.compose.project="+stackID+"; if ip link show dev "+host+" >/dev/null 2>&1; then echo interface-leaked; exit 1; fi")
	if err != nil || strings.TrimSpace(cleanup) != "" {
		t.Fatalf("resources remain: %s error=%v", cleanup, err)
	}
	if err := c.doJSON("DELETE", "/api/v1/settings/network-pools/"+pool.ID, c.token, "", nil, nil, name+"-pool-delete"); err != nil {
		t.Fatal(err)
	}
	pool.ID = ""
	stackID = ""
}

func caddyCompose(name, pool, node, marker string) string {
	return fmt.Sprintf(`name: %s
x-fl-network-pool: %s
x-fl-health-timeout: 90s
x-fl-stack:
  schema_version: 1
  primary_node: %s
  failover:
    mode: manual
  storage:
    pool: floatlab
    compression: lz4
    quota: 1G
services:
  caddy:
    image: caddy:2-alpine
    entrypoint: ["/bin/sh", "-ec"]
    command:
      - |
        printf '%%s\n' ':80 {' '  respond "%s"' '  log {' '    output stdout' '  }' '}' > /tmp/Caddyfile
        exec caddy run --config /tmp/Caddyfile --adapter caddyfile
    ports: ["8088:80"]
    volumes: ["data:/data", "config:/config"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost/"]
      interval: 2s
      timeout: 2s
      retries: 30
  probe:
    image: alpine:3.20
    command: ["sh", "-c", "while :; do sleep 60; done"]
volumes:
  data: {}
  config: {}
`, name, pool, node, marker)
}

func TestCaddyComposeFixture(t *testing.T) {
	source := caddyCompose("demo", "test-pool", "node1", "test-marker")
	if _, err := compose.ParseAndValidate(context.Background(), source, "demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := compose.RuntimeYAML(source, "demo", "10.254.240.2"); err != nil {
		t.Fatal(err)
	}
}
