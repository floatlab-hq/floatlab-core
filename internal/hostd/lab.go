package hostd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/lab"
	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"gopkg.in/yaml.v3"
)

const labDirectory = "/floatlab/system/workloads/lab"
const labHostDirectory = "/floatlab/system/etc/lab"
const labComposePath = "/floatlab/system/docker-compose.yml"
const labRQLiteDirectory = "/floatlab/system/management/rqlite"
const labRQLiteDataDirectory = labRQLiteDirectory + "/data"

type LabConfigureRequest struct {
	Mode   string         `json:"mode"`
	Bundle lab.JoinBundle `json:"bundle"`
}
type LabSSHRequest struct {
	Members []lab.Member `json:"members"`
	NodeID  string       `json:"node_id"`
	LabDir  string       `json:"lab_dir"`
}

func registerLabIPC(server *ipc.Server) {
	server.Handle("lab.validate-empty", func(ctx context.Context, _ json.RawMessage) (any, error) {
		return map[string]bool{"empty": true}, validateLabEmpty(ctx)
	})
	server.Handle("lab.ssh.host-key", func(context.Context, json.RawMessage) (any, error) {
		data, err := os.ReadFile("/etc/ssh/ssh_host_ed25519_key.pub")
		return map[string]string{"public_key": strings.TrimSpace(string(data))}, err
	})
	server.Handle("lab.configure", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req LabConfigureRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return map[string]string{"mode": req.Mode}, ConfigureLab(ctx, req)
	})
	server.Handle("lab.ssh.configure", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var req LabSSHRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		return map[string]string{"status": "applied"}, configureLabSSH(ctx, req)
	})
}

func validateLabEmpty(ctx context.Context) error {
	raw, err := labRun(ctx, "zfs", "list", "-H", "-t", "filesystem,volume,snapshot", "-o", "name", "-r", "floatlab")
	if err != nil {
		return err
	}
	if retained := labRetainedDatasets(strings.Fields(raw)); len(retained) > 0 {
		return fmt.Errorf("joining node retains workload or protection datasets: %s", strings.Join(retained, ", "))
	}
	root := "/floatlab/system/protection"
	if _, err = os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return fmt.Errorf("joining node retains protection material at %s", path)
		}
		return nil
	})
}

func labRetainedDatasets(names []string) []string {
	var retained []string
	for _, name := range names {
		if name == "floatlab" || name == "floatlab/stacks" || name == "floatlab/os" || strings.HasPrefix(name, "floatlab/os/") || strings.HasPrefix(name, "floatlab/os@") {
			continue
		}
		if name == "floatlab/system/protection" {
			continue
		}
		if strings.HasPrefix(name, "floatlab/system/protection/") || strings.HasPrefix(name, "floatlab/system/protection@") {
			retained = append(retained, name)
			continue
		}
		if name == "floatlab/system" || strings.HasPrefix(name, "floatlab/system/") || strings.HasPrefix(name, "floatlab/system@") {
			continue
		}
		retained = append(retained, name)
	}
	return retained
}

func labRun(ctx context.Context, name string, args ...string) (string, error) {
	data, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", name, err, data)
	}
	return strings.TrimSpace(string(data)), nil
}
func labCompose(ctx context.Context, args ...string) error {
	_, err := labRun(ctx, "docker", append([]string{"compose", "-p", "floatlab-system", "-f", labComposePath}, args...)...)
	return err
}

// ConfigureLab changes only the rqlite sidecar. Container workloads and the
// owner-local database remain running while lab consensus is reconfigured.
func ConfigureLab(ctx context.Context, req LabConfigureRequest) error {
	if req.Mode == "rotate" {
		return rotateLabIdentity(ctx, req.Bundle)
	}
	if req.Mode != "initialize" && req.Mode != "join" && req.Mode != "promote" {
		return errors.New("unknown lab configuration mode")
	}
	b := req.Bundle
	if req.Mode == "initialize" {
		if data, err := os.ReadFile(filepath.Join(labHostDirectory, "runtime.json")); err == nil {
			var previous lab.JoinBundle
			if err = json.Unmarshal(data, &previous); err != nil {
				return err
			}
			if previous.LabID != b.LabID || previous.NodeID != b.NodeID {
				return errors.New("persisted lab runtime identity mismatch")
			}
			if err = labCompose(ctx, "up", "-d", "--no-deps", "rqlite"); err != nil {
				return err
			}
			return waitLabRQLitePath(ctx, previous, "/readyz?noleader")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`).MatchString(b.NodeID) {
		return errors.New("invalid node ID")
	}
	for _, addr := range []string{b.RaftAddress, b.RQLiteAddress} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return err
		}
	}
	if req.Mode != "initialize" {
		if _, _, err := net.SplitHostPort(b.BootstrapRaftAddress); err != nil {
			return err
		}
	}
	if _, err := tls.X509KeyPair([]byte(b.CertificatePEM), []byte(b.PrivateKeyPEM)); err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(b.CAPEM)) {
		return errors.New("invalid CA")
	}
	unlock, err := update.Acquire(labHostDirectory)
	if err != nil {
		return err
	}
	defer unlock()
	for name, data := range map[string]string{"ca.pem": b.CAPEM, "node.pem": b.CertificatePEM, "node-key.pem": b.PrivateKeyPEM} {
		if err = update.WriteFile(filepath.Join(labHostDirectory, name), []byte(data), 0600); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(labComposePath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return errors.New("missing core services")
	}
	service, ok := services["rqlite"].(map[string]any)
	if !ok {
		return errors.New("missing rqlite sidecar")
	}
	if err = labCompose(ctx, "stop", "--timeout", "30", "rqlite"); err != nil {
		return err
	}
	if req.Mode != "promote" {
		backup := filepath.Join(labHostDirectory, "before-"+req.Mode+"-"+b.LabID)
		if err = lab.BackupDirectory(labRQLiteDirectory, backup); err != nil {
			return err
		}
		if err = update.WriteFile(backup+".compose.yml", data, 0600); err != nil {
			return err
		}
		if req.Mode == "join" {
			marker := filepath.Join(labHostDirectory, "joined-"+b.LabID)
			if _, e := os.Stat(marker); errors.Is(e, os.ErrNotExist) {
				// The standalone shared database is replaceable only after the
				// caller's empty-workload check and a verified full backup.
				entries, e := os.ReadDir(labRQLiteDirectory)
				if e != nil {
					return e
				}
				for _, entry := range entries {
					if e = os.RemoveAll(filepath.Join(labRQLiteDirectory, entry.Name())); e != nil {
						return e
					}
				}
				if err = update.WriteFile(marker, []byte("standalone backed up before joining\n"), 0600); err != nil {
					return err
				}
			} else if e != nil {
				return e
			}
		} else {
			// Existing standalone rqlite often used its Docker address as its
			// ID. Recover it explicitly to the appliance's permanent node ID.
			peers := []map[string]any{{"id": b.NodeID, "address": b.RaftAddress, "non_voter": false}}
			if err = update.WriteJSON(filepath.Join(labRQLiteDataDirectory, "raft", "peers.json"), peers); err != nil {
				return err
			}
		}
	}
	certRoot := "/floatlab-lab"
	// v8.26 disables reaping when flags are omitted; explicit 0s is rejected.
	command := []string{"-node-id=" + b.NodeID, "-http-addr=0.0.0.0:4001", "-http-adv-addr=" + b.RQLiteAddress, "-http-cert=" + certRoot + "/node.pem", "-http-key=" + certRoot + "/node-key.pem", "-http-ca-cert=" + certRoot + "/ca.pem", "-http-verify-client", "-raft-addr=0.0.0.0:4002", "-raft-adv-addr=" + b.RaftAddress, "-node-cert=" + certRoot + "/node.pem", "-node-key=" + certRoot + "/node-key.pem", "-node-ca-cert=" + certRoot + "/ca.pem", "-node-verify-client", "-raft-cluster-remove-shutdown=false"}
	if req.Mode != "initialize" {
		command = append(command, "-join="+b.BootstrapRaftAddress, "-join-attempts=30", "-join-interval=1s", fmt.Sprintf("-raft-non-voter=%t", req.Mode == "join"))
	}
	// The pinned image's shell entrypoint appends /rqlite/file/data itself.
	// Invoke the binary directly so there is exactly one explicit data path,
	// preserving the appliance's existing data/ subtree.
	service["entrypoint"] = []string{"/bin/rqlited"}
	command = append(command, "/rqlite/file/data")
	service["command"] = command
	volumes, _ := service["volumes"].([]any)
	found := false
	for _, v := range volumes {
		if text, ok := v.(string); ok && strings.Contains(text, ":"+certRoot) {
			found = true
		}
	}
	if !found {
		volumes = append(volumes, labHostDirectory+":"+certRoot+":ro")
	}
	service["volumes"] = volumes
	host, _, _ := net.SplitHostPort(b.RaftAddress)
	service["ports"] = []string{net.JoinHostPort(host, "4001") + ":4001", net.JoinHostPort(host, "4002") + ":4002"}
	// The ordinary image healthcheck uses anonymous HTTP. Readiness is checked
	// below with the pinned lab certificate instead.
	service["healthcheck"] = map[string]any{"disable": true}
	if control, ok := services["floatlab-control"].(map[string]any); ok {
		if dependencies, ok := control["depends_on"].(map[string]any); ok {
			if _, exists := dependencies["rqlite"]; exists {
				dependencies["rqlite"] = map[string]any{"condition": "service_started"}
			}
		}
	}
	data, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err = update.WriteFile(labComposePath, data, 0644); err != nil {
		return err
	}
	if err = update.WriteJSON(filepath.Join(labHostDirectory, "runtime.json"), b); err != nil {
		return err
	}
	if err = labCompose(ctx, "up", "-d", "--no-deps", "rqlite"); err != nil {
		return err
	}
	return waitLabRQLite(ctx, b)
}

func rotateLabIdentity(ctx context.Context, b lab.JoinBundle) error {
	unlock, err := update.Acquire(labHostDirectory)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := os.ReadFile(filepath.Join(labHostDirectory, "runtime.json"))
	if err != nil {
		return err
	}
	var previous lab.JoinBundle
	if err = json.Unmarshal(data, &previous); err != nil {
		return err
	}
	if previous.NodeID != b.NodeID || previous.LabID != b.LabID || previous.PeerURL != b.PeerURL || previous.RaftAddress != b.RaftAddress || previous.RQLiteAddress != b.RQLiteAddress {
		return errors.New("rotation cannot change member identity or addresses")
	}
	if _, err = tls.X509KeyPair([]byte(b.CertificatePEM), []byte(b.PrivateKeyPEM)); err != nil {
		return err
	}
	if previous.CAPEM != b.CAPEM {
		return errors.New("leaf rotation cannot change the lab trust root")
	}
	for name, content := range map[string]string{"node.pem": b.CertificatePEM, "node-key.pem": b.PrivateKeyPEM} {
		if err = update.WriteFile(filepath.Join(labHostDirectory, name), []byte(content), 0600); err != nil {
			return err
		}
	}
	previous.CertificatePEM = b.CertificatePEM
	previous.PrivateKeyPEM = b.PrivateKeyPEM
	if err = update.WriteJSON(filepath.Join(labHostDirectory, "runtime.json"), previous); err != nil {
		return err
	}
	if err = labCompose(ctx, "restart", "--timeout", "30", "rqlite"); err != nil {
		return err
	}
	return waitLabRQLite(ctx, previous)
}

func labHTTPClient(b lab.JoinBundle) (*http.Client, error) {
	cert, err := tls.X509KeyPair([]byte(b.CertificatePEM), []byte(b.PrivateKeyPEM))
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(b.CAPEM)) {
		return nil, errors.New("invalid lab CA")
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}, RootCAs: pool}}}, nil
}
func waitLabRQLite(ctx context.Context, b lab.JoinBundle) error {
	return waitLabRQLitePath(ctx, b, "/readyz")
}
func waitLabRQLitePath(ctx context.Context, b lab.JoinBundle, path string) error {
	client, err := labHTTPClient(b)
	if err != nil {
		return err
	}
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+b.RQLiteAddress+path, nil)
		resp, e := client.Do(req)
		if e == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("rqlite did not become ready with lab credentials")
		case <-ticker.C:
		}
	}
}

func configureLabSSH(ctx context.Context, req LabSSHRequest) error {
	if req.LabDir != labDirectory {
		return errors.New("invalid lab identity directory")
	}
	const wrapper = labHostDirectory + "/replica-receive.sh"
	// SSH supplies the command as an environment value; it is never evaluated
	// by a shell. The container receiver permits only replicate and status.
	script := `#!/bin/sh
set -eu
case "$1" in *[!A-Za-z0-9_-]*|'') exit 126;; esac
case "$SSH_ORIGINAL_COMMAND" in replicate|status) ;; *) exit 126;; esac
exec sudo -n /floatlab/system/etc/lab/replica-receive-root.sh "$1" "$SSH_ORIGINAL_COMMAND"
`
	if err := update.WriteFile(wrapper, []byte(script), 0755); err != nil {
		return err
	}
	// The unprivileged SSH receiver must traverse this directory; keys stay 0600.
	if err := os.Chmod(labHostDirectory, 0711); err != nil {
		return err
	}
	rootScript := `#!/bin/sh
set -eu
[ "$#" -eq 2 ] || exit 126
case "$1" in *[!A-Za-z0-9_-]*|'') exit 126;; esac
case "$2" in replicate|status) ;; *) exit 126;; esac
container="$(docker ps -q --filter label=com.docker.compose.project=floatlab-system --filter label=com.docker.compose.service=floatlab-control)"
[ -n "$container" ] || exit 126
exec docker exec -i -e "SSH_ORIGINAL_COMMAND=$2" "$container" floatlab-control replica-receive --source "$1"
`
	if err := update.WriteFile(labHostDirectory+"/replica-receive-root.sh", []byte(rootScript), 0755); err != nil {
		return err
	}
	// Nix declares this dedicated account and its sudo allowlist; do not grant
	// it generic docker-group access or a usable interactive login shell.
	if err := lab.ApplySSHTrust(req.Members, "/var/lib/floatlab-replica/.ssh/authorized_keys", filepath.Join(labDirectory, "known_hosts"), wrapper); err != nil {
		return err
	}
	account, err := user.Lookup("floatlab-replica")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	for _, path := range []string{"/var/lib/floatlab-replica", "/var/lib/floatlab-replica/.ssh", "/var/lib/floatlab-replica/.ssh/authorized_keys"} {
		if err = os.Chown(path, uid, gid); err != nil {
			return err
		}
	}
	return configureLabRevocations(ctx, req.NodeID, req.Members)
}

func configureLabRevocations(ctx context.Context, nodeID string, members []lab.Member) error {
	_, lookupErr := labRun(ctx, "nft", "list", "table", "inet", "floatlab_lab_revocations")
	rules, err := labFirewallRules(nodeID, members, lookupErr == nil)
	if err != nil {
		return err
	}
	path := filepath.Join(labHostDirectory, "revocations.nft")
	if err = update.WriteFile(path, []byte(rules), 0600); err != nil {
		return err
	}
	_, err = labRun(ctx, "nft", "-f", path)
	return err
}

func labFirewallRules(nodeID string, members []lab.Member, existing bool) (string, error) {
	// Docker-published ports traverse FORWARD, while native peer/SSH listeners
	// traverse INPUT. Enforce tombstones in both; a valid old CA is insufficient.
	var addresses4, addresses6 []string
	var removing4, removing6 []string
	var allowed4, allowed6 []string
	var ownIP net.IP
	for _, m := range members {
		if m.Status != "active" && m.Status != "enrolling" && m.Status != "removing" {
			continue
		}
		host, _, err := net.SplitHostPort(m.RaftAddress)
		if err != nil {
			return "", err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return "", errors.New("node transport requires literal advertised peer IPs")
		}
		if m.NodeID == nodeID {
			ownIP = ip
		}
		if ip.To4() != nil {
			allowed4 = append(allowed4, ip.String())
		} else {
			allowed6 = append(allowed6, ip.String())
		}
	}
	for _, m := range members {
		if (m.Status != "removed" && m.Status != "removing") || m.PeerAddressReleased {
			continue
		}
		host, _, err := net.SplitHostPort(m.RaftAddress)
		if err != nil {
			return "", err
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return "", errors.New("removed member must have a literal peer IP for firewall revocation")
		}
		if ip.To4() != nil {
			if m.Status == "removing" {
				removing4 = append(removing4, ip.String())
			} else {
				addresses4 = append(addresses4, ip.String())
			}
		} else {
			if m.Status == "removing" {
				removing6 = append(removing6, ip.String())
			} else {
				addresses6 = append(addresses6, ip.String())
			}
		}
	}
	var rules strings.Builder
	if existing {
		rules.WriteString("delete table inet floatlab_lab_revocations\n")
	}
	rules.WriteString("table inet floatlab_lab_revocations {\n chain input { type filter hook input priority -20; policy accept;\n")
	write := func() {
		for _, group := range []struct {
			family string
			ips    []string
			ports  string
		}{{"ip", addresses4, "22, 4001, 4002, 9443"}, {"ip6", addresses6, "22, 4001, 4002, 9443"}, {"ip", removing4, "22, 9443"}, {"ip6", removing6, "22, 9443"}} {
			if len(group.ips) > 0 {
				fmt.Fprintf(&rules, " %s saddr { %s } tcp dport { %s } reject\n", group.family, strings.Join(group.ips, ", "), group.ports)
			}
		}
	}
	write()
	rules.WriteString(" }\n chain forward { type filter hook forward priority -20; policy accept;\n")
	write()
	rules.WriteString(" }\n chain ingress { type filter hook prerouting priority -110; policy accept;\n")
	// Before Docker DNAT, restrict consensus and rqlite HTTPS to enrolled peer
	// IPs on this appliance's advertised interface. Bootstrap HTTPS remains
	// invitation-authenticated; ordinary administrator SSH is unaffected.
	if ownIP != nil {
		family := "ip"
		allowed := allowed4
		if ownIP.To4() == nil {
			family = "ip6"
			allowed = allowed6
		}
		fmt.Fprintf(&rules, " %s daddr %s tcp dport { 4001, 4002 } %s saddr != { %s } drop\n", family, ownIP.String(), family, strings.Join(allowed, ", "))
	}
	rules.WriteString(" }\n}\n")
	return rules.String(), nil
}

// RecoverLabOnHost runs only from the root CLI, independently of management.
func RecoverLabOnHost(ctx context.Context, req lab.RecoveryRequest) (lab.RecoveryStatus, error) {
	if os.Geteuid() != 0 {
		return lab.RecoveryStatus{}, errors.New("run as root")
	}
	data, err := os.ReadFile(filepath.Join(labHostDirectory, "runtime.json"))
	if err != nil {
		return lab.RecoveryStatus{}, err
	}
	var bundle lab.JoinBundle
	if err = json.Unmarshal(data, &bundle); err != nil {
		return lab.RecoveryStatus{}, err
	}
	client, err := labHTTPClient(bundle)
	if err != nil {
		return lab.RecoveryStatus{}, err
	}
	db := rqlite.NewClient("https://" + bundle.RQLiteAddress)
	db.ConfigureHTTP("https://"+bundle.RQLiteAddress, client)
	return lab.RecoverLab(ctx, lab.RecoveryConfig{LabDataDir: labDirectory, RQLiteDataDir: labRQLiteDataDirectory, JournalDir: filepath.Join(labHostDirectory, "recovery"), DB: db, FencePeers: func(ctx context.Context, enabled bool) error {
		return fenceLabRecoveryPeers(ctx, bundle.RaftAddress, enabled)
	}, Stop: func(ctx context.Context) error { return labCompose(ctx, "stop", "--timeout", "30", "rqlite") }, Start: func(ctx context.Context) error {
		if err := clearLabJoinFlags(); err != nil {
			return err
		}
		return labCompose(ctx, "up", "-d", "--no-deps", "rqlite")
	}, Ready: func(ctx context.Context) error { return waitLabRQLite(ctx, bundle) }, ApplyTrust: func(ctx context.Context, members []lab.Member) error {
		return configureLabSSH(ctx, LabSSHRequest{Members: members, NodeID: bundle.NodeID, LabDir: labDirectory})
	}}, req)
}

func labRecoveryFirewallRules(iface string, existing bool) (string, error) {
	if iface == "" || len(iface) > 15 || strings.ContainsAny(iface, "\"\\\n\r\x00") {
		return "", errors.New("invalid recovery peer interface")
	}
	var rules strings.Builder
	if existing {
		rules.WriteString("delete table inet floatlab_lab_recovery\n")
	}
	// Physical ingress runs before Docker DNAT; loopback and container traffic
	// do not enter this interface, so local administration stays available.
	fmt.Fprintf(&rules, "table inet floatlab_lab_recovery {\n chain ingress { type filter hook prerouting priority -120; policy accept;\n iifname \"%s\" tcp dport { 4001, 4002, 9443 } drop\n }\n}\n", iface)
	return rules.String(), nil
}

func fenceLabRecoveryPeers(ctx context.Context, address string, enabled bool) error {
	const path = labHostDirectory + "/recovery-quarantine.nft"
	_, lookupErr := labRun(ctx, "nft", "list", "table", "inet", "floatlab_lab_recovery")
	if !enabled {
		if lookupErr == nil {
			if _, err := labRun(ctx, "nft", "delete", "table", "inet", "floatlab_lab_recovery"); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		dir, err := os.Open(labHostDirectory)
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("recovery requires a literal peer IP")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	var name string
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			return err
		}
		for _, addr := range addresses {
			if network, ok := addr.(*net.IPNet); ok && network.IP.Equal(ip) {
				name = iface.Name
			}
		}
	}
	if name == "" || name == "lo" {
		return errors.New("recovery peer address must belong to a physical host interface")
	}
	rules, err := labRecoveryFirewallRules(name, false)
	if err != nil {
		return err
	}
	if err = update.WriteFile(path, []byte(rules), 0600); err != nil {
		return err
	}
	applyPath := path
	if lookupErr == nil {
		rules, err = labRecoveryFirewallRules(name, true)
		if err != nil {
			return err
		}
		applyPath = path + ".apply"
		if err = update.WriteFile(applyPath, []byte(rules), 0600); err != nil {
			return err
		}
	}
	_, err = labRun(ctx, "nft", "-f", applyPath)
	return err
}

func clearLabJoinFlags() error {
	data, err := os.ReadFile(labComposePath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err = yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		return errors.New("missing services")
	}
	service, ok := services["rqlite"].(map[string]any)
	if !ok {
		return errors.New("missing rqlite")
	}
	command, ok := service["command"].([]any)
	if !ok {
		return errors.New("rqlite command is not a configured argument vector")
	}
	filtered := make([]any, 0, len(command))
	for _, value := range command {
		arg, ok := value.(string)
		if !ok {
			return errors.New("invalid rqlite argument")
		}
		if strings.HasPrefix(arg, "-join") || strings.HasPrefix(arg, "-raft-non-voter") {
			continue
		}
		filtered = append(filtered, arg)
	}
	service["command"] = filtered
	data, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return update.WriteFile(labComposePath, data, 0644)
}
