package main

import (
	"context"
	"encoding/base64"

	"github.com/floatlab/floatlab-core/internal/integrationcoverage"

	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/control"
	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/failover"
	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/lab"
	"github.com/floatlab/floatlab-core/internal/orchestrator"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/operation"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

var (
	devMode             bool
	listenAddr          string
	uiDir               string
	rqliteURL           string
	workloadDBPath      string
	labPeerURL          string
	labListen           string
	labRaftAddress      string
	labRQLiteAddress    string
	labDataDir          string
	raftNodeID          string
	raftBindAddr        string
	raftAdvAddr         string
	raftDataDir         string
	raftBootstrap       bool
	vlogsURL            string
	vmetricsURL         string
	jwtSecret           string
	jwtIssuer           string
	jwtAudience         string
	registerLocalNode   bool
	bootstrapFile       string
	applianceConfig     string
	credentialKeyFile   string
	bootstrapUser       string
	bootstrapPass       string
	hostNodeID          string
	hostSocket          string
	preflight           bool
	selfTestMode        bool
	isolationTest       bool
	selfTestToken       string
	selfTestPersistence string
	expectedRelease     string
	releaseInfo         bool
	isolationProbes     []string
)

func main() {
	defer integrationcoverage.Flush()
	root := &cobra.Command{
		Use:   "floatlab-control",
		Short: "FloatLab control plane — state management, API, and failover orchestration",
		RunE:  run,
	}

	root.Flags().StringVar(&listenAddr, "listen", ":8080", "HTTP API and UI listen address")
	root.Flags().BoolVar(&devMode, "dev-mode", os.Getenv("FLOATLAB_DEV_MODE") == "true", "Identify this control plane as development mode")
	root.Flags().StringVar(&uiDir, "ui-dir", "ui/dist", "Built UI directory")
	root.Flags().StringVar(&workloadDBPath, "workload-db", "/floatlab/system/workloads/local.sqlite", "Node-owned SQLite workload database")
	root.Flags().StringVar(&labPeerURL, "lab-peer-url", os.Getenv("FLOATLAB_PEER_URL"), "Advertised HTTPS lab peer origin")
	root.Flags().StringVar(&labListen, "lab-listen", ":9443", "Lab mutual TLS peer listener")
	root.Flags().StringVar(&labRaftAddress, "lab-raft-address", os.Getenv("FLOATLAB_RQLITE_RAFT_ADDRESS"), "Reachable rqlite consensus address")
	root.Flags().StringVar(&labRQLiteAddress, "lab-rqlite-address", os.Getenv("FLOATLAB_RQLITE_ADDRESS"), "Reachable rqlite HTTP address")
	root.Flags().StringVar(&labDataDir, "lab-data", "/floatlab/system/workloads/lab", "Private lab identity and journals")
	addReplicaCommand(root)
	root.Flags().StringVar(&rqliteURL, "rqlite-url", "http://localhost:4001", "rqlite base URL")
	root.Flags().StringVar(&raftNodeID, "raft-id", "node1", "Unique Raft node ID")
	root.Flags().StringVar(&raftBindAddr, "raft-bind", "0.0.0.0:7000", "Raft TCP bind address")
	root.Flags().StringVar(&raftAdvAddr, "raft-advertise", "127.0.0.1:7000", "Raft TCP advertise address (externally reachable)")
	root.Flags().StringVar(&raftDataDir, "raft-data", "/var/lib/floatlab/raft", "Raft data directory for BoltDB")
	root.Flags().BoolVar(&raftBootstrap, "raft-bootstrap", false, "Bootstrap a new single-node Raft cluster")
	root.Flags().StringVar(&vlogsURL, "vlogs-url", "http://localhost:9428", "VictoriaLogs base URL")
	root.Flags().StringVar(&vmetricsURL, "vmetrics-url", "http://localhost:8428", "VictoriaMetrics base URL")
	root.Flags().StringVar(&jwtSecret, "jwt-secret", os.Getenv("FLOATLAB_JWT_SECRET"), "HMAC secret for management API JWTs")
	root.Flags().StringVar(&jwtIssuer, "jwt-issuer", os.Getenv("FLOATLAB_JWT_ISSUER"), "Required JWT issuer")
	root.Flags().StringVar(&jwtAudience, "jwt-audience", os.Getenv("FLOATLAB_JWT_AUDIENCE"), "Required JWT audience")
	root.Flags().BoolVar(&registerLocalNode, "register-local-node", false, "Register the appliance node through the internal bootstrap path")
	root.Flags().StringVar(&bootstrapFile, "bootstrap-file", os.Getenv("FLOATLAB_BOOTSTRAP_FILE"), "Private hashed administrator bootstrap file")
	root.Flags().StringVar(&applianceConfig, "appliance-config", os.Getenv("FLOATLAB_APPLIANCE_CONFIG"), "Private persistent appliance identity")
	root.Flags().StringVar(&credentialKeyFile, "credential-key-file", os.Getenv("FLOATLAB_CREDENTIAL_KEY_FILE"), "Private AES-256 credential encryption key")
	root.Flags().StringVar(&bootstrapUser, "bootstrap-user", os.Getenv("FLOATLAB_BOOTSTRAP_USER"), "Initial administrator username")
	root.Flags().StringVar(&bootstrapPass, "bootstrap-password", os.Getenv("FLOATLAB_BOOTSTRAP_PASSWORD"), "Initial administrator password")
	root.Flags().StringVar(&hostNodeID, "host-node-id", "node1", "Node ID served by the local host daemon")
	root.Flags().StringVar(&hostSocket, "host-socket", "/run/floatlab/hostd.sock", "Local host daemon socket")
	root.Flags().BoolVar(&preflight, "preflight", false, "Run isolated management checks without controllers")
	root.Flags().BoolVar(&selfTestMode, "self-test", false, "Check running local management dependencies and exit")
	root.Flags().BoolVar(&isolationTest, "isolation-test", false, "Verify preflight network and restricted IPC isolation")
	root.Flags().StringVar(&selfTestToken, "self-test-token", "", "Disposable preflight persistence token")
	root.Flags().StringVar(&selfTestPersistence, "self-test-persistence", "", "Preflight persistence phase: write or verify")
	root.Flags().StringVar(&expectedRelease, "expected-release", "", "Expected management release version")
	root.Flags().BoolVar(&releaseInfo, "release-info", false, "Print release compatibility information and exit")
	root.Flags().StringSliceVar(&isolationProbes, "isolation-probe", nil, "Known reachable host/production TCP endpoints that sandbox must deny")

	if err := root.Execute(); err != nil {
		integrationcoverage.Flush()
		os.Exit(1)
	}
}

func run(cmd *cobra.Command, args []string) error {
	log, _ := zap.NewProduction()
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if selfTestMode {
		return selfTest(ctx)
	}
	if releaseInfo {
		return printReleaseInfo()
	}

	var identity struct {
		NodeID    string `json:"node_id"`
		NodeName  string `json:"node_name"`
		JWTSecret string `json:"jwt_secret"`
	}
	if applianceConfig != "" {
		if err := credentials.ReadPrivate(applianceConfig, &identity); err != nil {
			return err
		}
		if identity.NodeID == "" || identity.NodeName == "" || len(identity.JWTSecret) < 32 {
			return fmt.Errorf("invalid appliance identity")
		}
		raftNodeID, hostNodeID, jwtSecret = identity.NodeID, identity.NodeID, identity.JWTSecret
	}
	var credentialKey []byte
	if credentialKeyFile != "" {
		info, err := os.Lstat(credentialKeyFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return fmt.Errorf("credential key must be private")
		}
		credentialKey, err = os.ReadFile(credentialKeyFile)
		if err != nil {
			return err
		}
		if len(credentialKey) != 32 {
			return fmt.Errorf("credential key must be 32 bytes")
		}
	}
	if !preflight {
		var shared map[string]string
		if err := credentials.ReadPrivate(labDataDir+"/auth.json", &shared); err == nil {
			if len(shared["jwt_secret"]) < 32 {
				return fmt.Errorf("invalid persisted lab authentication")
			}
			jwtSecret = shared["jwt_secret"]
			if encoded := shared["credential_key"]; encoded != "" {
				decoded, decodeErr := base64.StdEncoding.DecodeString(encoded)
				if decodeErr != nil || len(decoded) != 32 {
					return fmt.Errorf("invalid persisted lab credential key")
				}
				credentialKey = decoded
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// Lab configuration is consensus-backed; workload authority is always local.
	labDB := rqlite.NewClient(rqliteURL)
	if !preflight {
		if err := lab.PreparePendingIdentity(labDataDir); err != nil {
			return fmt.Errorf("restore pending lab identity: %w", err)
		}
	}
	if _, err := os.Stat(labDataDir + "/ca.pem"); err == nil && labPeerURL != "" {
		transport := &labTransport{}
		if err = transport.reload(labDataDir); err != nil {
			return err
		}
		labDB.ConfigureHTTP("https://rqlite:4001", &http.Client{Transport: transport, Timeout: 10 * time.Second})
	}
	labErr := initializeLab(ctx, labDB)
	if labErr != nil {
		log.Warn("lab configuration unavailable; using committed local cache", zap.Error(labErr))
	}
	db, err := openWorkloads(ctx, labDB)
	if err != nil {
		return err
	}
	defer db.Close()
	if labErr == nil {
		if err = cacheLab(ctx, labDB, db); err != nil {
			return err
		}
	}
	store := config.NewSplitStore(db, labDB)
	if err = importLifecycle(ctx, db, log); err != nil {
		return err
	}
	raftNode, err := floatraft.NewLocalNode(ctx, db, hostNodeID, log)
	if err != nil {
		return err
	}
	defer raftNode.Shutdown()

	// Host client pool.
	hosts := hostclient.NewPool(log)
	hosts.Register(hostNodeID, hostSocket)
	defer hosts.Close()
	if err := serveRuntimeChecks(ctx, raftNode); err != nil {
		return err
	}
	if preflight {
		// A cloned database may contain queued work and remote nodes; none may be activated.
		if err := os.Setenv("FLOATLAB_PREFLIGHT", "true"); err != nil {
			return err
		}
	}
	broker := notify.NewBroker()
	seq := failover.NewSequence(db, store, raftNode, hosts, broker, log)
	var srv *control.Server
	startControllers := func(controllerCtx context.Context) {
		ctx := controllerCtx
		var controllers sync.WaitGroup
		launch := func(fn func()) { controllers.Add(1); go func() { defer controllers.Done(); fn() }() }

		if err := ipam.MigratePools(ctx, db, hosts); err != nil {
			log.Warn("network pool migration incomplete", zap.Error(err))
		}
		if err := ipam.RestoreNodeServices(ctx, db, hosts, hostNodeID); err != nil {
			log.Warn("service address restoration incomplete", zap.Error(err))
		}
		// Retry ownership reconciliation after DHCP startup or hostd reconnection.
		launch(func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := ipam.RestoreNodeServices(ctx, db, hosts, hostNodeID); err != nil {
						log.Debug("service address reconciliation deferred", zap.Error(err))
					}
				}
			}
		})
		// Notification broker — fan-out SSE publisher.
		launch(func() { control.RunHostCompatibility(ctx, hosts, db, broker, log) })

		// Notification retention: purge read notifications older than 30 days, daily.
		launch(func() {
			t := time.NewTicker(24 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if err := notify.Cleanup(ctx, db); err != nil {
						log.Warn("notify cleanup", zap.Error(err))
					}
					if err := operation.Cleanup(ctx, db, time.Now().UTC().AddDate(-1, 0, 0)); err != nil {
						log.Warn("operation cleanup", zap.Error(err))
					}
				}
			}
		})

		// Cross-node failover is explicit; loss of reachability never grants ownership.

		// Orchestrator: drives stack state machine from Raft + IPC events.
		orch := orchestrator.New(store, raftNode, hosts, db, log)
		launch(func() {
			if err := orch.Run(ctx); err != nil && err != context.Canceled {
				log.Error("orchestrator exited", zap.Error(err))
			}
		})

		// Snapshot scheduler: minute-tick loop that enqueues snapshot tasks.
		launch(func() { orchestrator.RunScheduler(ctx, orch, db, log) })

		// Task worker: polls rqlite tasks table and dispatches IPC to hostd.
		w := worker.New(db, store, hosts, raftNode, log)
		w.SetBackupRestorePreparer(srv.PrepareBackupRestore)
		w.SetStackMoveHandler(srv.RunStackMove)
		launch(func() {
			if err := w.Run(ctx); err != nil && err != context.Canceled {
				log.Error("worker exited", zap.Error(err))
			}
		})
		controllers.Wait()
	}
	// HTTP control server.
	srv = control.NewServer(&control.Config{
		DevMode:       devMode,
		ListenAddr:    listenAddr,
		UIDir:         uiDir,
		RQLiteURL:     rqliteURL,
		VLogsURL:      vlogsURL,
		VMetricsURL:   vmetricsURL,
		JWTSecret:     jwtSecret,
		JWTIssuer:     jwtIssuer,
		JWTAudience:   jwtAudience,
		CredentialKey: credentialKey,
		LabDB:         labDB,
		LocalNodeID:   hostNodeID,
		RefreshLab:    func(ctx context.Context) error { return cacheLab(ctx, labDB, db) },
	}, db, store, raftNode, hosts, broker, seq, log)

	if !preflight {
		if err := startLab(ctx, srv, labDB, db, hosts, log); err != nil {
			return err
		}
		go superviseControllers(ctx, startControllers, log)
	}

	log.Info("floatlab-control starting",
		zap.String("listen", listenAddr),
		zap.String("raft_id", raftNodeID),
		zap.String("rqlite", rqliteURL),
		zap.String("vlogs", vlogsURL),
		zap.String("vmetrics", vmetricsURL),
	)

	return srv.Run(ctx)
}
