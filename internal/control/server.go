package control

import (
	"context"
	"fmt"
	"net/http"
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
	cfg    *Config
	router *chi.Mux
	log    *zap.Logger
	db     *rqlite.Client
	store  *config.Store
	raft   *floatraft.Node
	hosts  *hostclient.Pool
	sm     run.StateMachine
	broker *notify.Broker
	vlogs  *logs.Client
	vmets  *stats.Client
	seq    *failover.Sequence
	ops    *operation.Store
	auth   *authLimiter
}

type Config struct {
	ListenAddr  string
	RaftConfig  floatraft.Config
	RQLiteURL   string
	VLogsURL    string
	VMetricsURL string
	JWTSecret   string
	JWTIssuer   string
	JWTAudience string
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
		cfg:    cfg,
		log:    log,
		db:     db,
		store:  store,
		raft:   raftNode,
		hosts:  hosts,
		sm:     run.New(),
		broker: broker,
		vlogs:  logs.NewClient(cfg.VLogsURL),
		vmets:  stats.NewClient(cfg.VMetricsURL),
		seq:    seq,
		ops:    operation.NewStore(db),
		auth:   newAuthLimiter(),
	}
	s.router = s.buildRouter()
	return s
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(requestLogger(s.log))
	r.Use(pangolinIdentity)
	r.Use(chimiddleware.Recoverer)
	r.Get("/swagger", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger/", http.StatusMovedPermanently)
	})
	r.Handle("/swagger/*", http.StripPrefix("/swagger", managementapi.Handler()))

	r.Route("/api/v1", func(r chi.Router) {
		registerAuthRoutes(r, s)
		registerHealthRoutes(r, s)
		registerNodeRoutes(r, s)
		registerStackRoutes(r, s)
		registerStorageRoutes(r, s)
		registerFailoverRoutes(r, s)
		registerNetworkRoutes(r, s)
		registerLogRoutes(r, s)
		registerStatsRoutes(r, s)
		registerNotifyRoutes(r, s)
		registerEventRoutes(r, s)
	})

	return r
}

func (s *Server) Run(ctx context.Context) error {
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
