package control

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"

	managementapi "github.com/floatlab/floatlab-core/api/openapi"
	"github.com/floatlab/floatlab-core/internal/failover"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/logs"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/operation"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/floatlab/floatlab-core/pkg/stats"
)

type Server struct {
	startedAt     time.Time
	cfg           *Config
	router        *chi.Mux
	log           *zap.Logger
	db            *rqlite.Client
	store         *config.Store
	raft          *floatraft.Node
	hosts         *hostclient.Pool
	sm            run.StateMachine
	broker        *notify.Broker
	vlogs         *logs.Client
	vmets         *stats.Client
	seq           *failover.Sequence
	ops           *operation.Store
	auth          *authLimiter
	labHandler    http.Handler
	secretMu      sync.RWMutex
	labServer     *Server
	workloadDB    *rqlite.Client
	peerWorkloads *workloadPeers
	joinPending   func() bool
	membershipMu  sync.RWMutex
}

type Config struct {
	DevMode       bool
	ListenAddr    string
	UIDir         string
	RaftConfig    floatraft.Config
	RQLiteURL     string
	VLogsURL      string
	VMetricsURL   string
	JWTSecret     string
	JWTIssuer     string
	JWTAudience   string
	CredentialKey []byte
	LabDB         *rqlite.Client
	LocalNodeID   string
	RefreshLab    func(context.Context) error
}

func NewServer(
	cfg *Config,
	db *rqlite.Client,
	store *config.Store,
	raftNode *floatraft.Node,
	hosts *hostclient.Pool,
	broker *notify.Broker,
	seq *failover.Sequence,
	log *zap.Logger,
) *Server {
	s := &Server{
		startedAt:  time.Now(),
		cfg:        cfg,
		log:        log,
		db:         db,
		workloadDB: db,
		store:      store,
		raft:       raftNode,
		hosts:      hosts,
		sm:         run.New(),
		broker:     broker,
		vlogs:      logs.NewClient(cfg.VLogsURL),
		vmets:      stats.NewClient(cfg.VMetricsURL),
		seq:        seq,
		ops:        operation.NewStore(db),
		auth:       newAuthLimiter(),
	}
	s.router = s.buildRouter()
	if cfg.LabDB != nil {
		labCfg := *cfg
		labCfg.LabDB = nil
		s.labServer = NewServer(&labCfg, cfg.LabDB, config.NewStore(cfg.LabDB), raftNode, hosts, broker, seq, log)
		s.labServer.workloadDB = db
	}
	return s
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(requestLogger(s.log))
	r.Use(pangolinIdentity)
	r.Use(chimiddleware.Recoverer)
	r.Use(updateMutationFence)
	r.Use(s.labJoinFence)
	r.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusMovedPermanently)
	})
	r.Handle("/swagger/*", http.StripPrefix("/swagger", managementapi.Handler()))

	r.Route("/api/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAdminJWT)
			r.Get("/lab/status", s.serveLab)
			r.Get("/lab/members", s.serveLab)
			r.Post("/lab/invitations", s.serveLab)
			r.Post("/lab/join", s.serveLab)
			r.Post("/lab/members/{id}/remove", s.serveLab)
			r.Get("/lab/members/{id}/removal", s.serveLab)
			r.Post("/lab/members/{id}/release-peer-address", s.serveLab)
			r.Post("/lab/identity/rotate", s.serveLab)
		})
		registerAuthRoutes(r, s)
		registerHealthRoutes(r, s)
		r.Group(func(r chi.Router) { r.Use(s.requireAdminJWT); r.Use(s.idempotency); registerNodeRoutes(r, s) })
		registerStackRoutes(r, s)
		registerStorageRoutes(r, s)
		registerFailoverRoutes(r, s)
		registerNetworkRoutes(r, s)
		registerHostNetworkRoutes(r, s)
		registerUpdateRoutes(r, s)
		r.Group(func(r chi.Router) { r.Use(s.requireAdminJWT); registerLogRoutes(r, s) })
		r.Post("/stats/webhook", s.handleStatsWebhook)
		r.Group(func(r chi.Router) { r.Use(s.requireAdminJWT); registerStatsRoutes(r, s) })
		r.Group(func(r chi.Router) { r.Use(s.requireAdminJWT); r.Use(s.idempotency); registerNotifyRoutes(r, s) })
		r.Group(func(r chi.Router) { r.Use(s.requireAdminJWT); registerEventRoutes(r, s) })
		registerConfigRoutes(r, s)
	})

	if s.cfg.UIDir != "" {
		r.Mount("/", http.FileServer(http.Dir(s.cfg.UIDir)))
	}

	return r
}

func (s *Server) SetLabHandler(h http.Handler) { s.labHandler = h }

func (s *Server) Handler() http.Handler { return s.router }

func (s *Server) SetLabJoinGate(pending func() bool) { s.joinPending = pending }

func (s *Server) labJoinFence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.joinPending == nil {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/lab/join" {
			// Drain earlier writes before checking emptiness and switching lab credentials.
			s.membershipMu.Lock()
			defer s.membershipMu.Unlock()
		} else {
			s.membershipMu.RLock()
			defer s.membershipMu.RUnlock()
			if s.joinPending() {
				writeErrorCode(w, http.StatusConflict, "lab_join_pending", "lab enrollment is pending; mutations and login resume after configuration sync")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveLab(w http.ResponseWriter, r *http.Request) {
	if s.labHandler == nil {
		writeError(w, 503, "lab enrollment is not configured")
		return
	}
	s.labHandler.ServeHTTP(w, r)
}

func (s *Server) SetLabSecrets(jwtKey, encodedKey string) {
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	s.cfg.JWTSecret = jwtKey
	if key, err := base64.StdEncoding.DecodeString(encodedKey); err == nil && len(key) == 32 {
		s.cfg.CredentialKey = key
	}
	if s.labServer != nil {
		s.labServer.SetLabSecrets(jwtKey, encodedKey)
	}
}

func (s *Server) ExportLabSecrets() map[string]string {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	secrets := map[string]string{"jwt_secret": s.cfg.JWTSecret}
	if len(s.cfg.CredentialKey) > 0 {
		secrets["credential_key"] = base64.StdEncoding.EncodeToString(s.cfg.CredentialKey)
	}
	return secrets
}

// labMutation keeps shared writes on consensus while reads remain locally available.
func (s *Server) labMutation(selectHandler func(*Server) http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target := s
		if s.labServer != nil {
			target = s.labServer
		}
		selectHandler(target)(w, r)
		if s.cfg.RefreshLab != nil {
			if err := s.cfg.RefreshLab(r.Context()); err != nil {
				s.log.Warn("lab cache refresh deferred", zap.Error(err))
			}
		}
	}
}

func (s *Server) jwtKey() string {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	return s.cfg.JWTSecret
}
func (s *Server) credentialKey() []byte {
	s.secretMu.RLock()
	defer s.secretMu.RUnlock()
	return append([]byte(nil), s.cfg.CredentialKey...)
}

func (s *Server) labDatabase() *rqlite.Client {
	if s.cfg.LabDB != nil {
		return s.cfg.LabDB
	}
	return s.db
}

func (s *Server) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go s.runUpdateBackground(ctx)
	// Bridge FSM events → broker so SSE clients get independent channels.
	go s.bridgeFSMToBroker()

	srv := &http.Server{
		Addr:         s.cfg.ListenAddr,
		Handler:      s.router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE endpoints need no write timeout
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("control: listening", zap.String("addr", s.cfg.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("control: server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}
