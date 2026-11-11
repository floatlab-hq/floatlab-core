package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/floatlab/floatlab-core/internal/control"
	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/lab"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

const sqliteRsyncVersion = "3.50.4"

type labTransport struct {
	current atomic.Pointer[http.Transport]
}

func (t *labTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.current.Load().RoundTrip(r)
}
func (t *labTransport) reload(dir string) error {
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "node.pem"), filepath.Join(dir, "node-key.pem"))
	if err != nil {
		return err
	}
	ca, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return fmt.Errorf("invalid lab trust root")
	}
	next := http.DefaultTransport.(*http.Transport).Clone()
	next.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, Certificates: []tls.Certificate{cert}}
	if old := t.current.Swap(next); old != nil {
		old.CloseIdleConnections()
	}
	return nil
}

func addReplicaCommand(root *cobra.Command) {
	var source, dir string
	command := &cobra.Command{Use: "replica-receive", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return rqlite.ServeReplicaCommand(cmd.Context(), rqlite.ReplicaReceiver{Root: dir, SourceNodeID: source, SQLiteRsync: rqlite.SQLiteRsync{Executable: "/usr/local/bin/sqlite3_rsync", Version: sqliteRsyncVersion}}, os.Getenv("SSH_ORIGINAL_COMMAND"), os.Stdin, os.Stdout, os.Stderr)
	}}
	command.Flags().StringVar(&source, "source", "", "Authenticated source node")
	command.Flags().StringVar(&dir, "replica-dir", "/floatlab/system/workloads/replicas", "Read-only replica directory")
	root.AddCommand(command)
}

func startLab(ctx context.Context, srv *control.Server, labDB, local *rqlite.Client, hosts *hostclient.Pool, log *zap.Logger) error {
	if labPeerURL == "" {
		return nil
	}
	if labRaftAddress == "" || labRQLiteAddress == "" {
		return fmt.Errorf("lab peer mode requires reachable rqlite HTTP and consensus addresses")
	}
	transport := &labTransport{}
	transport.current.Store(http.DefaultTransport.(*http.Transport).Clone())
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	if _, err := os.Stat(filepath.Join(labDataDir, "ca.pem")); err == nil {
		if err = transport.reload(labDataDir); err != nil {
			return err
		}
		labDB.ConfigureHTTP("https://rqlite:4001", client)
	}
	keyPath, pub, err := lab.EnsureSSHIdentity(labDataDir)
	if err != nil {
		return err
	}
	var hostKey struct {
		PublicKey string `json:"public_key"`
	}
	keyRaw, err := hosts.Execute(ctx, hostNodeID, "lab.ssh.host-key", nil)
	if err != nil {
		return fmt.Errorf("read replication SSH host key: %w", err)
	}
	if err = json.Unmarshal(keyRaw, &hostKey); err != nil {
		return err
	}
	var service *lab.Service
	var trustMu sync.Mutex
	var configMu sync.Mutex
	var lastTrust string
	replicator := &rqlite.Replicator{DB: local, StateDir: filepath.Join(filepath.Dir(workloadDBPath), "replication"), Transfer: (rqlite.SQLiteRsync{Executable: "/usr/local/bin/sqlite3_rsync", Version: sqliteRsyncVersion}).Transfer}
	apply := func(mode string) func(context.Context, lab.JoinBundle) error {
		return func(ctx context.Context, b lab.JoinBundle) error {
			_, err := hosts.Execute(ctx, hostNodeID, "lab.configure", map[string]any{"mode": mode, "bundle": b})
			if err != nil {
				return err
			}
			if err = transport.reload(labDataDir); err != nil {
				return err
			}
			labDB.ConfigureHTTP("https://rqlite:4001", client)
			return nil
		}
	}
	cfg := lab.Config{NodeID: hostNodeID, PeerURL: labPeerURL, RaftAddress: labRaftAddress, RQLiteAddress: labRQLiteAddress, DataDir: labDataDir, RQLiteURL: "https://rqlite:4001", DB: labDB, HTTPClient: client, SSHHostKey: hostKey.PublicKey, SSHReplicationKey: pub,
		RequireEmpty: func(ctx context.Context) error {
			stacks, err := config.NewStore(local).ListStacks(ctx)
			if err != nil {
				return err
			}
			if len(stacks) > 0 {
				return fmt.Errorf("node has workloads")
			}
			points, err := local.Query(ctx, rqlite.Statement{SQL: `SELECT id FROM recovery_points LIMIT 1`})
			if err != nil {
				return err
			}
			if len(points.Values) > 0 {
				return fmt.Errorf("node has retained recovery material")
			}
			_, err = hosts.Execute(ctx, hostNodeID, "lab.validate-empty", nil)
			return err
		},
		ApplyJoin: apply("join"), Promote: apply("promote"), Rotate: apply("rotate"), LocalDB: local, ReplicaDir: filepath.Join(filepath.Dir(workloadDBPath), "replicas"),
		SyncConfig: func(ctx context.Context) error {
			configMu.Lock()
			defer configMu.Unlock()
			return cacheLab(ctx, labDB, local)
		},
		ExportSecrets: func(context.Context) (map[string]string, error) { return srv.ExportLabSecrets(), nil },
		ImportSecrets: func(ctx context.Context, secrets map[string]string) error {
			if len(secrets["jwt_secret"]) < 32 {
				return fmt.Errorf("invalid shared JWT secret")
			}
			if encoded := secrets["credential_key"]; encoded != "" {
				key, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil || len(key) != 32 {
					return fmt.Errorf("invalid shared credential key")
				}
			}
			if err := credentials.WritePrivate(filepath.Join(labDataDir, "auth.json"), secrets); err != nil {
				return err
			}
			if applianceConfig != "" {
				var identity map[string]any
				if err := credentials.ReadPrivate(applianceConfig, &identity); err != nil {
					return err
				}
				identity["jwt_secret"] = secrets["jwt_secret"]
				if err := credentials.WritePrivate(applianceConfig, identity); err != nil {
					return err
				}
			}
			if encoded := secrets["credential_key"]; encoded != "" && credentialKeyFile != "" {
				key, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil || len(key) != 32 {
					return fmt.Errorf("invalid shared credential key")
				}
				if err = credentials.WriteBytes(credentialKeyFile, key); err != nil {
					return err
				}
			}
			srv.SetLabSecrets(secrets["jwt_secret"], secrets["credential_key"])
			return nil
		},
		ApplyTrust: func(ctx context.Context, members []lab.Member) error {
			trustMu.Lock()
			defer trustMu.Unlock()
			body, _ := json.Marshal(members)
			if string(body) == lastTrust {
				return nil
			}
			_, err := hosts.Execute(ctx, hostNodeID, "lab.ssh.configure", map[string]any{"members": members, "node_id": hostNodeID, "lab_dir": labDataDir})
			if err != nil {
				return err
			}
			lastTrust = string(body)
			replicator.Wake()
			return nil
		},
	}
	service, err = lab.Open(ctx, cfg)
	if err != nil {
		return err
	}
	srv.SetLabJoinGate(service.JoinPending)
	if err = service.ResumeJoin(ctx); err != nil {
		return fmt.Errorf("resume lab enrollment: %w", err)
	}
	bundle, err := service.InitialBundle()
	if err != nil {
		return err
	}
	if err = apply("initialize")(ctx, bundle); err != nil {
		return fmt.Errorf("initialize rqlite peer transport: %w", err)
	}
	if err = service.RefreshTrust(ctx); err != nil {
		log.Warn("lab trust refresh deferred; using committed local membership", zap.Error(err))
	}
	replicator.Peers = func(context.Context) ([]rqlite.ReplicaPeer, error) {
		var peers []rqlite.ReplicaPeer
		for _, m := range service.Snapshot().Members {
			if m.NodeID == hostNodeID || m.Status != "active" {
				continue
			}
			u, err := url.Parse(m.PeerURL)
			if err != nil {
				return nil, err
			}
			peers = append(peers, rqlite.ReplicaPeer{NodeID: m.NodeID, Address: u.Hostname(), SSHUser: "floatlab-replica", IdentityFile: keyPath, KnownHostsFile: filepath.Join(labDataDir, "known_hosts")})
		}
		return peers, nil
	}
	admin := service.Handler()
	srv.SetLabHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.Clone(r.Context())
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/v1/lab")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		if r.Method == http.MethodGet && r.URL.Path == "/status" {
			refresh, cancelRefresh := context.WithTimeout(r.Context(), time.Second)
			_ = service.RefreshTrust(refresh)
			cancelRefresh()
			id, _ := local.LocalIdentity(r.Context())
			replicas := []rqlite.LocalIdentity{}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(workloadDBPath), "replicas", "*.sqlite"))
			for _, f := range files {
				if value, err := rqlite.ReadReplicaIdentity(r.Context(), f); err == nil {
					replicas = append(replicas, value)
				}
			}
			state := service.Snapshot()
			quorum := false
			probe, cancelProbe := context.WithTimeout(r.Context(), 500*time.Millisecond)
			if _, err := labDB.QueryStrong(probe, rqlite.Statement{SQL: `SELECT 1`}); err == nil {
				quorum = true
			}
			cancelProbe()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"lab_id": state.LabID, "node_id": hostNodeID, "members": state.Members, "quorum": quorum, "join_pending": service.JoinPending(), "local": id, "replicas": replicas, "trust_revision": state.TrustRevision, "applied_trust_revision": state.AppliedTrustRevision, "acknowledged_trust_revision": state.AcknowledgedTrustRevision})
			return
		}
		admin.ServeHTTP(w, r)
		if r.Method != http.MethodGet && !service.JoinPending() {
			if err := cacheLab(r.Context(), labDB, local); err != nil {
				log.Warn("lab cache refresh deferred", zap.Error(err))
			}
			replicator.Wake()
		}
	}))
	if err := srv.SetWorkloadPeers(cfg.ReplicaDir, client, func(nodeID string) (string, bool) {
		for _, member := range service.Snapshot().Members {
			if member.NodeID == nodeID && member.Status == "active" {
				return member.PeerURL, true
			}
		}
		return "", false
	}); err != nil {
		return err
	}
	peerHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/lab/enroll" {
			service.EnrollmentHandler().ServeHTTP(w, r)
			return
		}
		if err := service.CheckPeer(r.TLS); err != nil {
			http.Error(w, "peer is not an authorized lab member", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/backups/") {
			srv.ServeBackupPeer(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/stack-moves/") {
			srv.ServeStackMovePeer(w, r, r.TLS.PeerCertificates[0].Subject.CommonName)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/v1/storage/backups") || strings.HasPrefix(r.URL.Path, "/api/v1/storage/replication") || strings.HasPrefix(r.URL.Path, "/api/v1/stacks") || strings.HasPrefix(r.URL.Path, "/api/v1/operations/") || (r.Method == http.MethodGet && r.URL.Path == "/api/v1/nodes/"+hostNodeID+"/health") {
			srv.Handler().ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
	peer := &http.Server{Addr: labListen, ReadHeaderTimeout: 10 * time.Second, Handler: peerHandler, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return service.BootstrapTLSConfig() }}}
	go func() {
		if err := peer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Error("lab peer listener failed", zap.Error(err))
		}
	}()
	go func() {
		<-ctx.Done()
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = peer.Shutdown(stop)
	}()
	go func() {
		if err := replicator.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error("workload view replication failed", zap.Error(err))
		}
	}()
	// Lab configuration uses its consensus store; this monitor does not poll workload revisions.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				configMu.Lock()
				if service.JoinPending() {
					configMu.Unlock()
					continue
				}
				refresh, cancel := context.WithTimeout(ctx, 4*time.Second)
				_ = service.RefreshTrust(refresh)
				_ = cacheLab(refresh, labDB, local)
				cancel()
				configMu.Unlock()
			}
		}
	}()
	return nil
}
