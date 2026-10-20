package managementupdate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"gopkg.in/yaml.v3"
)

func (l *Linux) Preflight(ctx context.Context, s *Status) (result error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	meminfo, e := os.ReadFile("/proc/meminfo")
	if e != nil {
		return e
	}
	if e = checkSandboxMemory(string(meminfo)); e != nil {
		return e
	}
	name := "floatlab-preflight-" + s.OperationID
	if e := l.snapshot(ctx, s, name, false); e != nil {
		return e
	}
	root := "floatlab/system/update-tests/" + s.OperationID
	if _, e := l.run(ctx, "zfs", "create", "-p", "-o", "mountpoint=none", "floatlab/system/update-tests"); e != nil {
		if _, check := l.run(ctx, "zfs", "list", "floatlab/system/update-tests"); check != nil {
			return e
		}
	}
	for _, ds := range s.Datasets {
		target := root + strings.TrimPrefix(ds.Name, managementDataset)
		mount := "/" + target
		if _, e := l.run(ctx, "zfs", "clone", "-o", "mountpoint="+mount, ds.Name+"@"+name, target); e != nil {
			return e
		}
	}
	directory := filepath.Join(l.Dir, s.OperationID)
	if e := os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	// Preserve consensus identity without sharing appliance credentials with candidate code.
	b, e := os.ReadFile("/floatlab/system/etc/appliance.json")
	if e != nil {
		return e
	}
	var identity struct {
		NodeID string `json:"node_id"`
	}
	if e = json.Unmarshal(b, &identity); e != nil || identity.NodeID == "" {
		return fmt.Errorf("invalid appliance identity")
	}
	secret := make([]byte, 32)
	if _, e = rand.Read(secret); e != nil {
		return e
	}
	identityPath := filepath.Join(directory, "appliance.json")
	if e = update.WriteJSON(identityPath, map[string]string{"node_id": identity.NodeID, "node_name": "preflight", "jwt_secret": hex.EncodeToString(secret)}); e != nil {
		return e
	}
	project := "floatlab-test-" + strings.ToLower(s.OperationID)
	bridge := "ft" + hex.EncodeToString(secret[:5])
	network := project + "-isolated"
	table := "floatlab_" + hex.EncodeToString(secret[:5])
	// Persist cleanup identities before creating host network resources.
	if e = update.WriteJSON(filepath.Join(directory, "sandbox.json"), map[string]string{"network": network, "table": table}); e != nil {
		return e
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		path := filepath.Join(directory, "preflight.yml")
		if _, e := os.Stat(path); e == nil {
			if e = l.compose(cleanupCtx, path, project, "down", "--timeout", "20"); result == nil {
				result = e
			}
		}
		if _, e = l.run(cleanupCtx, "docker", "network", "rm", network); result == nil {
			result = e
		}
		if _, e = l.run(cleanupCtx, "nft", "delete", "table", "inet", table); result == nil {
			result = e
		}
	}()
	if _, e = l.run(ctx, "docker", "network", "create", "--internal", "--driver", "bridge", "--opt", "com.docker.network.bridge.name="+bridge, "--ipv6=false", network); e != nil {
		return e
	}
	probe, e := net.Listen("tcp4", "0.0.0.0:0")
	if e != nil {
		return e
	}
	defer probe.Close()
	go func() {
		for {
			conn, e := probe.Accept()
			if e != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	gateway, e := l.run(ctx, "docker", "network", "inspect", "--format", "{{(index .IPAM.Config 0).Gateway}}", network)
	if e != nil || net.ParseIP(gateway) == nil {
		return fmt.Errorf("sandbox gateway unavailable")
	}
	_, port, _ := net.SplitHostPort(probe.Addr().String())
	hostProbe := net.JoinHostPort(gateway, port)
	// Filter before Docker's own accept rules. Internal peers are allowed, host and all other networks are denied.
	rules := sandboxFirewallRules(table, bridge)
	rulesPath := filepath.Join(directory, "isolation.nft")
	if e = update.WriteFile(rulesPath, []byte(rules), 0600); e != nil {
		return e
	}
	if _, e = l.run(ctx, "nft", "-f", rulesPath); e != nil {
		return e
	}
	if _, e = l.run(ctx, "nft", "list", "table", "inet", table); e != nil {
		return e
	}
	doc := sandboxCompose(s, root, identityPath, identity.NodeID, network)
	b, e = yaml.Marshal(doc)
	if e != nil {
		return e
	}
	path := filepath.Join(directory, "preflight.yml")
	if e = update.WriteFile(path, b, 0600); e != nil {
		return e
	}
	if e = l.compose(ctx, path, project, "up", "-d", "--pull", "never"); e != nil {
		return e
	}
	runTests := func(persistence string) error {
		for {
			ids, e := l.run(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service=floatlab-control")
			if e == nil && strings.TrimSpace(ids) != "" {
				_, e = l.run(ctx, "docker", "exec", strings.TrimSpace(ids), "/usr/local/bin/floatlab-control", "--self-test", "--isolation-test", "--isolation-probe="+hostProbe, "--self-test-token="+s.OperationID, "--self-test-persistence="+persistence, "--expected-release="+s.Manifest.Version, "--host-socket=/run/preflight/hostd.sock", "--rqlite-url=http://rqlite:4001", "--vlogs-url=http://victoria-logs:9428", "--vmetrics-url=http://victoria-metrics:8428")
				if e == nil {
					return nil
				}
			} else if e == nil {
				e = fmt.Errorf("candidate control container is not running")
			}
			select {
			case <-ctx.Done():
				return fmt.Errorf("preflight failed: %w", e)
			case <-time.After(3 * time.Second):
			}
		}
	}
	if e = runTests("write"); e != nil {
		return e
	}
	if e = l.compose(ctx, path, project, "stop", "--timeout", "30"); e != nil {
		return e
	}
	if e = l.compose(ctx, path, project, "up", "-d", "--pull", "never"); e != nil {
		return e
	}
	return runTests("verify")
}

func sandboxFirewallRules(table, bridge string) string {
	return fmt.Sprintf(`table inet %s {
 chain input {
  type filter hook input priority -200; policy accept;
  iifname %q drop
 }
 chain forward {
  type filter hook forward priority -200; policy accept;
  iifname %q oifname != %q drop
 }
}
`, table, bridge, bridge, bridge)
}

func checkSandboxMemory(meminfo string) error {
	for _, line := range strings.Split(meminfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "MemAvailable:" && fields[2] == "kB" {
			available, e := strconv.ParseUint(fields[1], 10, 64)
			if e != nil {
				return e
			}
			if available < ((4<<30)+(512<<20))/1024 {
				return fmt.Errorf("preflight needs 4 GiB sandbox memory plus 512 MiB host headroom")
			}
			return nil
		}
	}
	return fmt.Errorf("MemAvailable unavailable; refusing to risk production memory")
}

func sandboxCompose(s *Status, root, identityPath, nodeID, network string) map[string]any {
	volumes := func(name, target string) []any {
		return []any{map[string]any{"type": "bind", "source": "/" + root + "/" + name, "target": target}}
	}
	service := func(image string) map[string]any {
		return map[string]any{"image": image, "restart": "no", "networks": []string{"sandbox"}, "dns": []string{"127.0.0.1"}, "cap_drop": []string{"ALL"}, "security_opt": []string{"no-new-privileges:true"}, "pids_limit": 256, "mem_limit": "1g", "cpus": 1, "sysctls": map[string]string{"net.ipv6.conf.all.disable_ipv6": "1"}}
	}
	ss := map[string]any{}
	rq := service(s.Manifest.Images["rqlite"])
	rq["command"] = []string{"-node-id=" + nodeID, "-raft-addr=rqlite:4002", "-raft-adv-addr=rqlite:4002"}
	rq["volumes"] = volumes("rqlite", "/rqlite/file")
	rq["healthcheck"] = map[string]any{"test": []string{"CMD-SHELL", "wget -qO- http://localhost:4001/readyz"}, "interval": "2s", "timeout": "5s", "retries": 60}
	ss["rqlite"] = rq
	vm := service(s.Manifest.Images["victoria-metrics"])
	vm["command"] = []string{"-storageDataPath=/victoria-metrics-data", "-retentionPeriod=90d"}
	vm["volumes"] = volumes("victoria-metrics", "/victoria-metrics-data")
	ss["victoria-metrics"] = vm
	vl := service(s.Manifest.Images["victoria-logs"])
	vl["command"] = []string{"-storageDataPath=/victoria-logs-data", "-retentionPeriod=30d"}
	vl["volumes"] = volumes("victoria-logs", "/victoria-logs-data")
	ss["victoria-logs"] = vl
	control := service(s.Manifest.Images["floatlab-control"])
	control["user"] = "0:0"
	control["depends_on"] = map[string]any{"rqlite": map[string]string{"condition": "service_healthy"}, "victoria-metrics": map[string]string{"condition": "service_started"}, "victoria-logs": map[string]string{"condition": "service_started"}}
	control["command"] = []string{"--preflight", "--listen=:8080", "--rqlite-url=http://rqlite:4001", "--raft-id=" + nodeID, "--raft-bind=0.0.0.0:7000", "--raft-advertise=floatlab-control:7000", "--raft-data=/var/lib/floatlab/raft", "--vlogs-url=http://victoria-logs:9428", "--vmetrics-url=http://victoria-metrics:8428", "--host-node-id=" + nodeID, "--host-socket=/run/preflight/hostd.sock"}
	control["environment"] = map[string]string{"FLOATLAB_APPLIANCE_CONFIG": "/config/appliance.json", "FLOATLAB_UPDATE_RELEASE": s.TargetRelease}
	control["volumes"] = append(volumes("raft", "/var/lib/floatlab/raft"), map[string]any{"type": "bind", "source": identityPath, "target": "/config/appliance.json", "read_only": true}, map[string]any{"type": "bind", "source": "/run/floatlab/preflight", "target": "/run/preflight", "read_only": true})
	ss["floatlab-control"] = control
	return map[string]any{"services": ss, "networks": map[string]any{"sandbox": map[string]any{"external": true, "name": network}}}
}
