package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
)

// The guest agent remains reachable when a fixture's network is partitioned.
// Run all requests locally in the guest, including authentication and provisioning.
type labGuest struct {
	t                  *testing.T
	name               string
	address            string
	root               string
	client             *apiClient
	pool               string
	firstIP            netip.Addr
	lastIP             netip.Addr
	standaloneUser     string
	standalonePassword string
}

func TestMultiNodeLab(t *testing.T) {
	if os.Getenv("FLOATLAB_MULTI_NODE_INTEGRATION") != "1" {
		t.Skip("run scripts/test-multi-node.sh for real multi-node appliance acceptance")
	}
	if os.Getenv("API_URL") != "" || os.Getenv("VM_NAME") != "" || os.Getenv("VM_SSH") != "" {
		t.Fatal("multi-node acceptance requires fresh owned appliances, not existing API/VM targets")
	}
	if os.Getenv("FLOATLAB_MULTI_NODE_RUN_ID") == "" {
		t.Setenv("FLOATLAB_MULTI_NODE_RUN_ID", fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	first := newLabGuest(t, 1)
	firstID := first.nodeID()
	first.configurePool(1, firstID)
	web := first.createWeb(firstID, "join-existing-web")
	second := newLabGuest(t, 2)
	var secondWeb string
	labScenario(t, "join-empty-node-to-populated-lab", []*labGuest{first, second}, func(t *testing.T) {
		second.join(first)
		assertLabMembers(t, 2, first, second)
		if first.nodeID() == second.nodeID() {
			t.Fatal("enrollment duplicated the source identity")
		}
		second.configurePool(2, second.nodeID())
		secondWeb = second.createWeb(second.nodeID(), "joined-node-web")
		first.assertWeb(web, "join-existing-web")
		assertLabCaughtUp(t, first, second)
		var stacks []struct {
			ID string `json:"id"`
		}
		if err := first.json("GET", "/api/v1/stacks", nil, &stacks); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, stack := range stacks {
			seen[stack.ID] = true
		}
		if !seen[web] || !seen[secondWeb] {
			t.Fatalf("aggregate view missing locally or remotely owned web stack: %+v", stacks)
		}
		stopped := first.client.mutate("POST", "/api/v1/stacks/"+secondWeb+"/stop", nil, "application/json")
		second.client.waitOperation(stopped.OperationID)
		second.client.waitStackState(secondWeb, "Idle")
		started := first.client.mutate("POST", "/api/v1/stacks/"+secondWeb+"/start", nil, "application/json")
		second.client.waitOperation(started.OperationID)
		second.client.waitStackState(secondWeb, "RunningPrimary")
		second.assertWeb(secondWeb, "joined-node-web")
		rotation := fmt.Sprintf("rotate-%d", time.Now().UnixNano())
		waitFor(t, 2*time.Minute, func() error {
			var status struct {
				Phase string `json:"phase"`
			}
			if err := second.json("POST", "/api/v1/lab/identity/rotate", map[string]string{"operation_id": rotation}, &status); err != nil {
				return err
			}
			if status.Phase != "complete" {
				return fmt.Errorf("joined identity rotation at %s", status.Phase)
			}
			return nil
		})
		assertLabCaughtUp(t, first, second)
	})
	if t.Failed() {
		return
	}
	secondID := second.nodeID()
	labScenario(t, "two-node-partition-local-workloads-and-recovery", []*labGuest{first, second}, func(t *testing.T) {
		second.link(false)
		first.assertSharedWritesUnavailable()
		second.assertSharedWritesUnavailable()
		first.stopStart(web, "join-existing-web")
		for index, node := range []*labGuest{first, second} {
			owner := []string{firstID, secondID}[index]
			name := fmt.Sprintf("two-node-partition-%d", index)
			id := node.createWeb(owner, name)
			node.stopStart(id, name)
			deleted := node.createWeb(owner, name+"-deleted")
			node.deleteStack(deleted)
		}
		if out, err := second.guest("control=$(docker ps -q --filter label=com.docker.compose.project=floatlab-system --filter label=com.docker.compose.service=floatlab-control); test -n \"$control\"; docker restart \"$control\""); err != nil {
			t.Fatalf("restart isolated management process: %s %v", out, err)
		}
		second.client.waitReady()
		second.client.login()
		second.assertStandaloneCredentialsRejected()
		second.stopStart(secondWeb, "joined-node-web")
		var isolatedStacks []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := second.json("GET", "/api/v1/stacks", nil, &isolatedStacks); err != nil {
			t.Fatalf("isolated restart lost local stack access: %v", err)
		}
		found := false
		for _, stack := range isolatedStacks {
			if stack.Name == "two-node-partition-1" {
				second.stopStart(stack.ID, stack.Name)
				found = true
			}
		}
		if !found {
			t.Fatal("isolated restart lost authoritative workload state")
		}
		if out, err := second.guest("docker compose -f /floatlab/system/docker-compose.yml stop rqlite"); err != nil {
			t.Fatalf("stop isolated local rqlite: %s %v", out, err)
		}
		t.Cleanup(func() { _, _ = second.guest("docker compose -f /floatlab/system/docker-compose.yml start rqlite") })
		withoutRqlite := second.createWeb(secondID, "isolated-without-rqlite")
		second.stopStart(withoutRqlite, "isolated-without-rqlite")
		second.deleteStack(withoutRqlite)
		if out, err := second.guest("docker compose -f /floatlab/system/docker-compose.yml start rqlite"); err != nil {
			t.Fatalf("restart isolated local rqlite: %s %v", out, err)
		}
		second.link(true)
		assertLabMembers(t, 2, first, second)
		assertLabCaughtUp(t, first, second)
	})
	if t.Failed() {
		return
	}
	third := newLabGuest(t, 3)
	third.join(first)
	assertLabMembers(t, 3, first, second, third)
	thirdID := third.nodeID()
	third.configurePool(3, thirdID)
	labScenario(t, "three-node-partition-majority-and-minority-recovery", []*labGuest{first, second, third}, func(t *testing.T) {
		third.link(false)
		third.assertSharedWritesUnavailable()
		for index, node := range []*labGuest{first, second, third} {
			owner := []string{firstID, secondID, thirdID}[index]
			name := fmt.Sprintf("three-node-partition-%d", index)
			id := node.createWeb(owner, name)
			node.stopStart(id, name)
			deleted := node.createWeb(owner, name+"-deleted")
			node.deleteStack(deleted)
		}
		// A real shared configuration mutation must still commit on the majority.
		var invitation map[string]any
		if err := first.json("POST", "/api/v1/lab/invitations", map[string]any{}, &invitation); err != nil {
			t.Fatalf("majority cannot commit lab configuration: %v", err)
		}
		var network struct {
			ID string `json:"id"`
		}
		if err := second.json("POST", "/api/v1/networks", map[string]string{"name": "partition-majority-network", "cidr": "fdfe:3234::/64"}, &network); err != nil {
			t.Fatalf("second majority node cannot update shared network configuration: %v", err)
		}
		third.link(true)
		assertLabMembers(t, 3, first, second, third)
		waitFor(t, 90*time.Second, func() error {
			var networks []struct {
				ID string `json:"id"`
			}
			if err := third.json("GET", "/api/v1/networks", nil, &networks); err != nil {
				return err
			}
			for _, row := range networks {
				if row.ID == network.ID {
					return nil
				}
			}
			return fmt.Errorf("healed minority has not applied shared network %s", network.ID)
		})
		assertLabCaughtUp(t, first, second, third)
	})
	if t.Failed() {
		return
	}
	labScenario(t, "remove-unavailable-node-with-quorum", []*labGuest{first, second, third}, func(t *testing.T) {
		third.link(false)
		var preview struct {
			Stale     bool             `json:"stale"`
			Workloads []map[string]any `json:"workloads"`
		}
		if err := first.json("GET", "/api/v1/lab/members/"+thirdID+"/removal", nil, &preview); err != nil || !preview.Stale || len(preview.Workloads) == 0 {
			t.Fatalf("unavailable-node removal preview omitted stale workload inventory: %+v %v", preview, err)
		}
		var result map[string]any
		if err := first.json("POST", "/api/v1/lab/members/"+thirdID+"/remove", map[string]string{"confirm_node_id": thirdID, "operation_id": fmt.Sprintf("remove-%d", time.Now().UnixNano())}, &result); err != nil {
			t.Fatal(err)
		}
		assertLabMembers(t, 2, first, second)
		third.link(true)
		assertLabMembers(t, 2, first, second)
		assertRemovedPeerRejected(t, third, first)
		if out, err := first.guest("test -r " + shellQuote("/floatlab/system/workloads/replicas/"+thirdID+".sqlite")); err != nil {
			t.Fatalf("removal lost retained workload replica: %s %v", out, err)
		}
		assertLabCaughtUp(t, first, second)
	})
	if t.Failed() {
		return
	}
	labScenario(t, "two-node-lost-quorum-fenced-recovery-and-return", []*labGuest{first, second}, func(t *testing.T) {
		second.link(false)
		first.assertSharedWritesUnavailable()
		// Fencing is performed by this fixture, not inferred from failed pings.
		if out, err := second.guest("docker compose -f /floatlab/system/docker-compose.yml stop rqlite"); err != nil {
			t.Fatalf("fence excluded rqlite: %s %v", out, err)
		}
		var result map[string]any
		if err := first.json("POST", "/api/v1/lab/members/"+secondID+"/remove", map[string]string{"confirm_node_id": secondID, "operation_id": fmt.Sprintf("ordinary-rejected-%d", time.Now().UnixNano())}, &result); err == nil {
			t.Fatal("ordinary node removal unexpectedly bypassed missing quorum")
		}
		operation := fmt.Sprintf("recovery-%d", time.Now().UnixNano())
		command := "floatlab-installer-hostd lab-recover --operation-id " + shellQuote(operation) + " --survivor " + shellQuote(firstID) + " --exclude " + shellQuote(secondID)
		if out, err := first.guest(command); err == nil {
			t.Fatalf("recovery accepted without fencing confirmation: %s", out)
		}
		command += " --confirm-fenced"
		out, err := first.guest(command)
		if err != nil {
			t.Fatalf("explicit fenced lab recovery: %s %v", out, err)
		}
		if out, err := first.guest(command); err != nil {
			t.Fatalf("repeated recovery is not idempotent: %s %v", out, err)
		}
		assertLabMembers(t, 1, first)
		first.assertWeb(web, "join-existing-web")
		var invitation map[string]any
		if err := first.json("POST", "/api/v1/lab/invitations", map[string]any{}, &invitation); err != nil {
			t.Fatalf("recovered survivor cannot commit shared configuration: %v", err)
		}
		retained := "/floatlab/system/workloads/replicas/" + secondID + ".sqlite"
		backup := "/floatlab/system/etc/lab/recovery/backup-" + operation + "/rqlite.manifest.json"
		if out, err := first.guest("set -eu; test -r " + shellQuote(retained) + "; test -s " + shellQuote(backup)); err != nil {
			t.Fatalf("recovery failed to retain replica or verified configuration backup: %s %v", out, err)
		}
		if out, err := second.guest("docker compose -f /floatlab/system/docker-compose.yml up -d --no-deps rqlite"); err != nil {
			t.Fatalf("return fenced fixture's old consensus process: %s %v", out, err)
		}
		second.link(true)
		assertRemovedPeerRejected(t, second, first)
		assertLabMembers(t, 1, first)
		// An old populated identity must not turn a fresh invitation into a rejoin.
		err = second.json("POST", "/api/v1/lab/join", map[string]any{"invitation": invitation, "peer_url": "https://" + second.host() + ":9443", "raft_address": second.host() + ":4002", "rqlite_address": second.host() + ":4001"}, &result)
		if err == nil {
			t.Fatal("removed populated appliance rejoined without explicit reset")
		}
		assertLabMembers(t, 1, first)
	})
}

func labScenario(t *testing.T, name string, nodes []*labGuest, body func(*testing.T)) {
	t.Helper()
	t.Run(name, func(child *testing.T) {
		for _, node := range nodes {
			node.t, node.client.t = child, child
		}
		defer func() {
			for _, node := range nodes {
				node.t, node.client.t = t, t
			}
		}()
		body(child)
	})
}

func assertRemovedPeerRejected(t *testing.T, removed, survivor *labGuest) {
	t.Helper()
	if out, err := removed.guest("set -eu; test -s /floatlab/system/etc/lab/node.pem; test -s /floatlab/system/etc/lab/node-key.pem; test -s /floatlab/system/etc/lab/ca.pem; test -s /floatlab/system/workloads/lab/replication-key; test -s /floatlab/system/workloads/lab/known_hosts"); err != nil {
		t.Fatalf("removed-node credential fixture missing: %s %v", out, err)
	}
	for _, endpoint := range []string{
		"https://" + survivor.host() + ":9443/lab/status",
		"https://" + survivor.host() + ":4001/status",
	} {
		out, err := removed.guest("curl --silent --show-error --max-time 5 --cert /floatlab/system/etc/lab/node.pem --key /floatlab/system/etc/lab/node-key.pem --cacert /floatlab/system/etc/lab/ca.pem -o /dev/null -w '%{http_code}' " + shellQuote(endpoint))
		if err == nil && strings.HasPrefix(strings.TrimSpace(out), "2") {
			t.Fatalf("removed identity still authorized at %s: HTTP %s", endpoint, out)
		}
	}
	ssh := "control=$(docker ps -q --filter label=com.docker.compose.project=floatlab-system --filter label=com.docker.compose.service=floatlab-control); test -n \"$control\"; " +
		"docker exec \"$control\" ssh -T -o BatchMode=yes -o IdentitiesOnly=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o UserKnownHostsFile=/floatlab/system/workloads/lab/known_hosts -i /floatlab/system/workloads/lab/replication-key " + shellQuote("floatlab-replica@"+survivor.host()) + " status"
	if out, err := removed.guest(ssh); err == nil {
		t.Fatalf("removed identity still authorized for database replication: %s", out)
	}
}

func newLabGuest(t *testing.T, index int) *labGuest {
	t.Helper()
	t.Setenv("API_URL", "")
	name := fmt.Sprintf("floatlab-multinode-%s-%d", os.Getenv("FLOATLAB_MULTI_NODE_RUN_ID"), index)
	t.Setenv("VM_NAME", name)
	if _, err := command(15*time.Second, "..", nil, "virsh", "-c", env("LIBVIRT_URI", "qemu:///system"), "dominfo", name); err == nil {
		t.Fatalf("refusing to replace an existing domain %s", name)
	}
	address := startAppliance(t)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	n := &labGuest{t: t, name: name, address: address, root: root}
	n.client = newAPIClient(t, "http://127.0.0.1:8080")
	n.client.client = &http.Client{Transport: n, Timeout: 65 * time.Second}
	t.Cleanup(func() {
		if t.Failed() {
			out, err := n.guest("docker ps -a; journalctl -u floatlab-hostd --no-pager -n 100; docker compose -f /floatlab/system/docker-compose.yml logs --tail=100 floatlab-control rqlite")
			t.Logf("%s diagnostics:\n%s\n%v", name, out, err)
		}
	})
	n.client.waitReady()
	// Only this newly created, empty fixture is reset; never touch user appliances.
	identity := fmt.Sprintf("integration-node-%d", index)
	configuration := fmt.Sprintf("FLOATLAB_NODE_ID=%s\nFLOATLAB_PEER_URL=https://%s:9443\nFLOATLAB_RQLITE_RAFT_ADDRESS=%s:4002\nFLOATLAB_RQLITE_ADDRESS=%s:4001\n", identity, n.host(), n.host(), n.host())
	jwtSecret := fmt.Sprintf("floatlab-multi-node-%s-node-%d-integration-secret", os.Getenv("FLOATLAB_MULTI_NODE_RUN_ID"), index)
	n.standaloneUser, n.standalonePassword = "demo", "floatlab"
	if index > 1 {
		n.standaloneUser = fmt.Sprintf("standalone-node-%d", index)
		n.standalonePassword = fmt.Sprintf("standalone-%s-node-%d-password", os.Getenv("FLOATLAB_MULTI_NODE_RUN_ID"), index)
	}
	// Release bundles freeze Compose interpolation, so seed the frozen values too.
	substitutions := "sed -i -e " + shellQuote("s/--host-node-id=node1/--host-node-id="+identity+"/g") +
		" -e " + shellQuote("s/-node-id=node1/-node-id="+identity+"/g") +
		" -e " + shellQuote("s|FLOATLAB_PEER_URL:.*|FLOATLAB_PEER_URL: https://"+n.host()+":9443|") +
		" -e " + shellQuote("s|FLOATLAB_RQLITE_RAFT_ADDRESS:.*|FLOATLAB_RQLITE_RAFT_ADDRESS: "+n.host()+":4002|") +
		" -e " + shellQuote("s|FLOATLAB_RQLITE_ADDRESS:.*|FLOATLAB_RQLITE_ADDRESS: "+n.host()+":4001|") +
		" -e " + shellQuote("s/FLOATLAB_JWT_SECRET:.*/FLOATLAB_JWT_SECRET: "+jwtSecret+"/") +
		" -e " + shellQuote("s/FLOATLAB_BOOTSTRAP_USER:.*/FLOATLAB_BOOTSTRAP_USER: "+n.standaloneUser+"/") +
		" -e " + shellQuote("s/FLOATLAB_BOOTSTRAP_PASSWORD:.*/FLOATLAB_BOOTSTRAP_PASSWORD: "+n.standalonePassword+"/") + " /floatlab/system/docker-compose.yml; "
	reset := "systemctl stop floatlab-core-stack; docker compose -f /floatlab/system/docker-compose.yml down; " +
		"for path in /floatlab/system/management/rqlite /floatlab/system/workloads /floatlab/system/etc/lab; do if test -d \"$path\"; then find \"$path\" -mindepth 1 -delete; fi; done; " +
		"printf %s " + shellQuote(configuration) + " > /floatlab/system/.env; " +
		substitutions +
		"mkdir -p /run/systemd/system/floatlab-hostd.service.d; " +
		"printf %s " + shellQuote("[Service]\nEnvironment=FLOATLAB_NODE_ID="+identity+"\n") + " > /run/systemd/system/floatlab-hostd.service.d/integration.conf; " +
		"systemctl daemon-reload; systemctl restart floatlab-hostd; systemctl start --no-block floatlab-core-stack"
	if out, err := n.guest("set -eu; " + reset); err != nil {
		t.Fatalf("initialize unique empty fixture identity: %s %v", out, err)
	}
	n.client.waitReady()
	var standaloneToken struct {
		AccessToken string `json:"access_token"`
	}
	credentials := map[string]string{"username": n.standaloneUser, "password": n.standalonePassword}
	if err := n.client.doJSON("POST", "/api/v1/auth/token", "", "application/json", bytes.NewReader(mustJSON(t, credentials)), &standaloneToken); err != nil {
		t.Fatalf("authenticate standalone fixture: %v", err)
	}
	if standaloneToken.AccessToken == "" {
		t.Fatal("standalone fixture returned an empty token")
	}
	n.client.token = standaloneToken.AccessToken
	if out, err := n.guest("docker pull caddy:2-alpine"); err != nil {
		t.Fatalf("preload partition workload image: %s %v", out, err)
	}
	return n
}

func (n *labGuest) guest(script string) (string, error) {
	// These appliance tests deliberately run serially; guestCommand uses this env.
	previous := os.Getenv("VM_NAME")
	if err := os.Setenv("VM_NAME", n.name); err != nil {
		return "", err
	}
	defer os.Setenv("VM_NAME", previous)
	return guestCommand(n.root, script)
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (n *labGuest) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	var err error
	if request.Body != nil {
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
	}
	args := "curl --silent --show-error --max-time 35 -X " + shellQuote(request.Method)
	for key, values := range request.Header {
		for _, value := range values {
			args += " -H " + shellQuote(key+": "+value)
		}
	}
	if request.Body != nil {
		args += " --data-binary @-"
	}
	args += " -w '\\n%{http_code}' " + shellQuote(request.URL.String())
	out, err := n.guest("printf %s " + shellQuote(base64.StdEncoding.EncodeToString(body)) + " | base64 -d | " + args)
	if err != nil {
		return nil, fmt.Errorf("%s guest API: %w", n.name, err)
	}
	position := strings.LastIndexByte(out, '\n')
	if position < 0 {
		return nil, fmt.Errorf("guest curl response has no HTTP status: %q", out)
	}
	status, err := strconv.Atoi(strings.TrimSpace(out[position+1:]))
	if err != nil || status < 100 {
		return nil, fmt.Errorf("guest curl invalid HTTP status: %q", out[position+1:])
	}
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(out[:position])), Request: request}, nil
}

func (n *labGuest) link(up bool) {
	n.t.Helper()
	raw, err := command(15*time.Second, n.root, nil, "virsh", "-c", env("LIBVIRT_URI", "qemu:///system"), "domiflist", n.name)
	if err != nil {
		n.t.Fatal(err)
	}
	var mac string
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 5 && fields[1] == "network" {
			mac = fields[4]
			break
		}
	}
	if mac == "" {
		n.t.Fatalf("no fixture network interface in %s", raw)
	}
	state := "down"
	if up {
		state = "up"
	}
	run(n.t, n.root, nil, "virsh", "-c", env("LIBVIRT_URI", "qemu:///system"), "domif-setlink", n.name, mac, state)
	if !up {
		n.t.Cleanup(func() { n.link(true) })
	}
}

func (n *labGuest) json(method, path string, body any, target any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = strings.NewReader(string(raw))
	}
	return n.client.doJSON(method, path, n.client.token, "application/json", reader, target, fmt.Sprintf("multinode-%d", time.Now().UnixNano()))
}

func (n *labGuest) assertSharedWritesUnavailable() {
	n.t.Helper()
	waitFor(n.t, 45*time.Second, func() error {
		response, body, err := n.client.request("POST", "/api/v1/lab/invitations", n.client.token, fmt.Sprintf("quorum-check-%d", time.Now().UnixNano()), "application/json", strings.NewReader("{}"))
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusServiceUnavailable {
			return fmt.Errorf("shared write returned %d %s, want 503 without quorum", response.StatusCode, body)
		}
		return nil
	})
}

func (n *labGuest) host() string {
	address, err := url.Parse(n.address)
	if err != nil {
		n.t.Fatal(err)
	}
	return address.Hostname()
}

func labWebCompose(name, owner, marker, pool string) string {
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
    quota: 128M
services:
  web:
    image: caddy:2-alpine
    entrypoint: ["/bin/sh", "-ec"]
    command: ["mkdir -p /data; printf '%%s' '%s' > /data/index.html; exec caddy file-server --root /data --listen :8080"]
    volumes: ["data:/data"]
    ports: ["8088:8080"]
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/"]
      interval: 2s
      timeout: 2s
      retries: 30
volumes:
  data: {}
`, name, pool, owner, marker)
}

func (n *labGuest) configurePool(index int, owner string) {
	n.t.Helper()
	address, err := netip.ParseAddr(n.host())
	if err != nil || !address.Is4() {
		n.t.Fatalf("fixture has no IPv4 LAN: %s %v", n.host(), err)
	}
	prefix := netip.PrefixFrom(address, 24).Masked()
	first := prefix.Addr().As4()
	first[3] = byte(128 + (index-1)*16)
	last := first
	last[3] += 15
	n.firstIP, n.lastIP = netip.AddrFrom4(first), netip.AddrFrom4(last)
	n.pool = fmt.Sprintf("multi-node-pool-%d", index)
	var created map[string]any
	if err := n.json("POST", "/api/v1/settings/network-pools", map[string]any{
		"name": n.pool, "cidr": prefix.String(), "start_ip": n.firstIP.String(), "end_ip": n.lastIP.String(), "node_ids": []string{owner},
	}, &created); err != nil {
		n.t.Fatalf("configure node's preassigned address range: %v", err)
	}
}

func (n *labGuest) createWeb(owner, name string) string {
	n.t.Helper()
	started := n.client.startNewStack(name, labWebCompose(name, owner, name, n.pool))
	n.client.waitOperation(started.OperationID)
	n.client.waitStackState(started.StackID, "RunningPrimary")
	n.assertWeb(started.StackID, name)
	return started.StackID
}

func (n *labGuest) assertWeb(id, marker string) {
	n.t.Helper()
	// A local HTTP probe proves the service runs, rather than trusting API state.
	script := "container=$(docker ps -q --filter label=com.docker.compose.project=" + shellQuote(id) + " --filter label=com.docker.compose.service=web); test -n \"$container\"; docker exec \"$container\" wget -qO- http://127.0.0.1:8080/"
	out, err := n.guest(script)
	if err != nil || out != marker {
		n.t.Fatalf("%s HTTP: %q, %v", id, out, err)
	}
	var status struct {
		StackIP string `json:"stack_ip"`
	}
	if err := n.json("GET", "/api/v1/stacks/"+id+"/status", nil, &status); err != nil {
		n.t.Fatal(err)
	}
	address, err := netip.ParseAddr(status.StackIP)
	if err != nil || address.Compare(n.firstIP) < 0 || address.Compare(n.lastIP) > 0 {
		n.t.Fatalf("stack address %s outside owner range %s-%s: %v", status.StackIP, n.firstIP, n.lastIP, err)
	}
	out, err = n.guest("curl --max-time 5 --fail --silent --show-error " + shellQuote("http://"+status.StackIP+":8088/"))
	if err != nil || out != marker {
		n.t.Fatalf("published HTTP %s: %q %v", status.StackIP, out, err)
	}
}

func (n *labGuest) stopStart(id, marker string) {
	n.t.Helper()
	stopped := n.client.mutate(http.MethodPost, "/api/v1/stacks/"+id+"/stop", nil, "application/json")
	n.client.waitOperation(stopped.OperationID)
	n.client.waitStackState(id, "Idle")
	started := n.client.mutate(http.MethodPost, "/api/v1/stacks/"+id+"/start", nil, "application/json")
	n.client.waitOperation(started.OperationID)
	n.client.waitStackState(id, "RunningPrimary")
	n.assertWeb(id, marker)
}

func (n *labGuest) deleteStack(id string) {
	n.t.Helper()
	deleted := n.client.mutate(http.MethodDelete, "/api/v1/stacks/"+id+"?purge=true", nil, "")
	n.client.waitOperation(deleted.OperationID)
	n.client.waitStackDeleted(id)
}

func TestMultiNodeWebFixture(t *testing.T) {
	if _, err := compose.ParseAndValidate(t.Context(), labWebCompose("partition-web", "node-a", "partition-marker", "node-pool"), "partition-web"); err != nil {
		t.Fatal(err)
	}
}

type labMembership struct {
	LabID   string `json:"lab_id"`
	NodeID  string `json:"node_id"`
	Members []struct {
		NodeID string `json:"node_id"`
		Status string `json:"status"`
	} `json:"members"`
}

func (n *labGuest) membership() (labMembership, error) {
	var result labMembership
	err := n.json("GET", "/api/v1/lab/members", nil, &result)
	return result, err
}

func (n *labGuest) nodeID() string {
	n.t.Helper()
	result, err := n.membership()
	if err != nil || result.NodeID == "" {
		n.t.Fatalf("local lab identity: %+v %v", result, err)
	}
	return result.NodeID
}

func (n *labGuest) join(source *labGuest) {
	n.t.Helper()
	var invitation map[string]any
	if err := source.json("POST", "/api/v1/lab/invitations", map[string]any{}, &invitation); err != nil {
		n.t.Fatal(err)
	}
	var member map[string]any
	err := n.json("POST", "/api/v1/lab/join", map[string]any{
		"invitation":     invitation,
		"peer_url":       "https://" + n.host() + ":9443",
		"raft_address":   n.host() + ":4002",
		"rqlite_address": n.host() + ":4001",
	}, &member)
	if err != nil {
		n.t.Fatal(err)
	}
	n.client.login()
	n.assertStandaloneCredentialsRejected()
}

func (n *labGuest) assertStandaloneCredentialsRejected() {
	n.t.Helper()
	if n.standaloneUser == "demo" {
		return
	}
	credentials := map[string]string{"username": n.standaloneUser, "password": n.standalonePassword}
	response, body, err := n.client.request("POST", "/api/v1/auth/token", "", "", "application/json", bytes.NewReader(mustJSON(n.t, credentials)))
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		n.t.Fatalf("old standalone credentials accepted after enrollment/restart: %v %v %s", response, err, body)
	}
}

func assertLabMembers(t *testing.T, count int, nodes ...*labGuest) {
	t.Helper()
	for _, node := range nodes {
		waitFor(t, 90*time.Second, func() error {
			members, err := node.membership()
			if err != nil {
				return err
			}
			active := 0
			for _, member := range members.Members {
				if member.Status == "active" {
					active++
				}
			}
			if active != count {
				return fmt.Errorf("%s has %d active members, want %d: %+v", node.name, active, count, members)
			}
			return nil
		})
	}
}

func (n *labGuest) sql(path, query string) (string, error) {
	return n.guest("sqlite3 -readonly -json " + shellQuote(path) + " " + shellQuote(query))
}

// Compare independently read SQLite content, not publisher acknowledgements.
func assertLabCaughtUp(t *testing.T, nodes ...*labGuest) {
	t.Helper()
	const metadata = "SELECT node_id,generation,revision FROM workload_identity WHERE singleton=1"
	const content = "SELECT * FROM stacks ORDER BY id"
	for _, source := range nodes {
		raw, err := source.sql("/floatlab/system/workloads/local.sqlite", metadata)
		if err != nil {
			t.Fatal(err)
		}
		var expected []struct {
			NodeID     string `json:"node_id"`
			Generation string `json:"generation"`
			Revision   int64  `json:"revision"`
		}
		if err := json.Unmarshal([]byte(raw), &expected); err != nil || len(expected) != 1 {
			t.Fatalf("source metadata %s: %v", raw, err)
		}
		rows, err := source.sql("/floatlab/system/workloads/local.sqlite", content)
		if err != nil {
			t.Fatal(err)
		}
		var stacks []struct {
			ID string `json:"id"`
		}
		if strings.TrimSpace(rows) != "" {
			if err := json.Unmarshal([]byte(rows), &stacks); err != nil {
				t.Fatal(err)
			}
		}
		related := make(map[string]string)
		for _, query := range []string{
			"SELECT * FROM workload_lifecycle ORDER BY stack_id",
			"SELECT * FROM operations ORDER BY id",
			"SELECT * FROM stack_runtime ORDER BY stack_id",
			"SELECT * FROM network_allocations ORDER BY id",
			"SELECT * FROM lifecycle_events ORDER BY id",
		} {
			value, err := source.sql("/floatlab/system/workloads/local.sqlite", query)
			if err != nil {
				t.Fatal(err)
			}
			related[query] = value
		}
		for _, target := range nodes {
			if target == source {
				continue
			}
			path := "/floatlab/system/workloads/replicas/" + expected[0].NodeID + ".sqlite"
			waitFor(t, 2*time.Minute, func() error {
				actual, err := target.sql(path, metadata)
				if err != nil {
					return err
				}
				var got []struct {
					NodeID     string `json:"node_id"`
					Generation string `json:"generation"`
					Revision   int64  `json:"revision"`
				}
				if err := json.Unmarshal([]byte(actual), &got); err != nil || len(got) != 1 {
					return fmt.Errorf("replica metadata %s: %v", actual, err)
				}
				if got[0].NodeID != expected[0].NodeID || got[0].Generation != expected[0].Generation || got[0].Revision < expected[0].Revision {
					return fmt.Errorf("%s replica %+v behind %+v", target.name, got[0], expected[0])
				}
				actualRows, err := target.sql(path, content)
				if err != nil || strings.TrimSpace(actualRows) != strings.TrimSpace(rows) {
					return fmt.Errorf("replica stack content mismatch: %s vs %s: %v", actualRows, rows, err)
				}
				for query, expectedRows := range related {
					actualRows, err := target.sql(path, query)
					if err != nil || strings.TrimSpace(actualRows) != strings.TrimSpace(expectedRows) {
						return fmt.Errorf("replica content mismatch for %s: %s vs %s: %v", query, actualRows, expectedRows, err)
					}
				}
				return nil
			})
			for _, stack := range stacks {
				out, err := target.guest("docker ps -aq --filter label=com.docker.compose.project=" + shellQuote(stack.ID))
				if err != nil || strings.TrimSpace(out) != "" {
					t.Fatalf("%s executed peer %s stack %s: %q %v", target.name, source.name, stack.ID, out, err)
				}
			}
		}
	}
}
