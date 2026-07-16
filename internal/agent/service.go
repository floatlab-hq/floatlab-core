package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/store"
)

type Config struct {
	SocketPath         string
	DockerSocketPath   string
	VictoriaLogsURL    string
	ForwardDmesg       bool
	NodeID             string
	VictoriaMetricsURL string
	MetricsInterval    time.Duration
	ForwardMetrics     bool
}

type Service struct {
	cfg        Config
	logger     *slog.Logger
	zfs        ZFS
	setup      SetupValidator
	httpServer *http.Server
	forwarder  *DmesgForwarder
	metrics    *MetricsForwarder
}

func NewService(cfg Config, logger *slog.Logger) *Service {
	zfs := CommandZFS{}
	return &Service{
		cfg:    cfg,
		logger: logger,
		zfs:    zfs,
		setup:  CommandSetupValidator{ZFS: zfs},
	}
}

func NewServiceWithZFS(cfg Config, logger *slog.Logger, zfs ZFS) *Service {
	return &Service{
		cfg:    cfg,
		logger: logger,
		zfs:    zfs,
		setup:  CommandSetupValidator{ZFS: zfs},
	}
}

func NewServiceWithDependencies(cfg Config, logger *slog.Logger, zfs ZFS, setup SetupValidator) *Service {
	return &Service{
		cfg:    cfg,
		logger: logger,
		zfs:    zfs,
		setup:  setup,
	}
}

func (s *Service) Bootstrap(ctx context.Context) error {
	if err := setupReportError(s.setup.RunSetupChecks(ctx)); err != nil {
		return err
	}

	if err := BootstrapSystemDatasets(ctx, s.zfs); err != nil {
		return err
	}

	if s.cfg.ForwardDmesg {
		forwarder, err := NewDmesgForwarder(s.cfg.VictoriaLogsURL, s.logger)
		if err != nil {
			return err
		}
		s.forwarder = forwarder
		go forwarder.Run(ctx)
	}
	if s.cfg.ForwardMetrics {
		if s.cfg.NodeID == "" {
			return errors.New("node id is required when metrics forwarding is enabled")
		}
		url := s.cfg.VictoriaMetricsURL
		if url == "" {
			url = "http://127.0.0.1:8428"
		}
		s.metrics = &MetricsForwarder{
			NodeID: s.cfg.NodeID,
			URL:    url,
			ZFS:    s.zfs,
			Logger: s.logger,
		}
		go s.metrics.Run(ctx, s.cfg.MetricsInterval)
	}

	return nil
}

func (s *Service) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.cfg.SocketPath), 0o755); err != nil {
		return err
	}
	if err := removeStaleSocket(s.cfg.SocketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o660); err != nil {
		_ = listener.Close()
		return err
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)
	s.httpServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdownCtx)
	}()

	return s.httpServer.Serve(listener)
}

func (s *Service) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

func (s *Service) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /setup/checks", s.handleSetupChecks)
	mux.HandleFunc("POST /zfs/bootstrap", s.handleBootstrap)
	mux.HandleFunc("GET /zfs/datasets", s.handleListDatasets)
	mux.HandleFunc("GET /zfs/dataset", s.handleListDatasets)
	mux.HandleFunc("GET /zfs/dataset/{dataset...}", s.handleGetDataset)
	mux.HandleFunc("PUT /zfs/dataset/{dataset...}", s.handleCreateDataset)
	mux.HandleFunc("DELETE /zfs/dataset/{dataset...}", s.handleDeleteDataset)
	mux.HandleFunc("GET /zfs/snapshots/{dataset...}", s.handleListSnapshots)
	mux.HandleFunc("PUT /zfs/snapshot/{target...}", s.handleCreateSnapshot)
	mux.HandleFunc("DELETE /zfs/snapshot/{target...}", s.handleDeleteSnapshot)
	mux.HandleFunc("GET /zfs/send/{target...}", s.handleSendSnapshot)
	mux.HandleFunc("PUT /zfs/receive/{dataset...}", s.handleReceiveSnapshot)
	mux.Handle("/docker/", s.dockerProxy())
}

func (s *Service) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleSetupChecks(w http.ResponseWriter, r *http.Request) {
	report := s.setup.RunSetupChecks(r.Context())
	status := http.StatusOK
	if !report.OK {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, report)
}

func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if err := BootstrapSystemDatasets(r.Context(), s.zfs); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ErrFloatlabPoolMissing) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{
		"datasets": {FloatlabSystemDataset, FloatlabLogsDataset, FloatlabMetricsDataset},
	})
}

func (s *Service) handleListDatasets(w http.ResponseWriter, r *http.Request) {
	datasets, err := s.zfs.ListDatasets(r.Context())
	if err != nil {
		writeZFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]store.Dataset{"datasets": datasets})
}

func (s *Service) handleGetDataset(w http.ResponseWriter, r *http.Request) {
	dataset, err := s.zfs.GetDataset(r.Context(), r.PathValue("dataset"))
	if err != nil {
		writeZFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, dataset)
}

func (s *Service) handleCreateDataset(w http.ResponseWriter, r *http.Request) {
	var req store.CreateDatasetRequest
	if r.Body != nil {
		defer drainAndClose(r.Body)
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	if err := s.zfs.CreateDataset(r.Context(), r.PathValue("dataset"), req); err != nil {
		writeZFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleDeleteDataset(w http.ResponseWriter, r *http.Request) {
	recursive, err := strictBool(r, "recursive")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.zfs.DeleteDataset(r.Context(), r.PathValue("dataset"), recursive); err != nil {
		writeZFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	recursive, err := strictBool(r, "recursive")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	snapshots, err := s.zfs.ListSnapshots(r.Context(), r.PathValue("dataset"), recursive)
	if err != nil {
		writeZFSError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string][]store.Snapshot{"snapshots": snapshots})
}

func (s *Service) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	s.handleSnapshotMutation(w, r, s.zfs.CreateSnapshot)
}

func (s *Service) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	s.handleSnapshotMutation(w, r, s.zfs.DeleteSnapshot)
}

func (s *Service) handleSnapshotMutation(w http.ResponseWriter, r *http.Request, fn func(context.Context, string, string, bool) error) {
	recursive, err := strictBool(r, "recursive")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dataset, snapshot, err := splitSnapshotTarget(r.PathValue("target"))
	if err != nil {
		writeZFSError(w, err)
		return
	}
	if err := fn(r.Context(), dataset, snapshot, recursive); err != nil {
		writeZFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleSendSnapshot(w http.ResponseWriter, r *http.Request) {
	recursive, err := strictBool(r, "recursive")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	dataset, snapshot, err := splitSnapshotTarget(r.PathValue("target"))
	if err != nil {
		writeZFSError(w, err)
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	sw.Header().Set("Content-Type", "application/octet-stream")
	if err := s.zfs.SendSnapshot(r.Context(), dataset, snapshot, r.URL.Query().Get("from"), recursive, sw); err != nil {
		if !sw.wrote {
			writeZFSError(w, err)
			return
		}
		s.logger.Error("zfs send failed", "err", err)
	}
}

func (s *Service) handleReceiveSnapshot(w http.ResponseWriter, r *http.Request) {
	force, err := strictBool(r, "forceRollback")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	defer drainAndClose(r.Body)
	if err := s.zfs.ReceiveSnapshot(r.Context(), r.PathValue("dataset"), force, r.Body); err != nil {
		writeZFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) dockerProxy() http.Handler {
	target := &url.URL{Scheme: "http", Host: "docker"}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", s.cfg.DockerSocketPath)
		},
	}
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = "docker"
		req.URL.Path = "/" + strings.TrimPrefix(req.URL.Path, "/docker/")
		req.Host = "docker"
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		writeError(w, http.StatusBadGateway, err)
	}
	return proxy
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func writeZFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBadZFSName):
		writeError(w, http.StatusBadRequest, err)
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, err)
	case errors.Is(err, errConflict):
		writeError(w, http.StatusConflict, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func strictBool(r *http.Request, key string) (bool, error) {
	value := r.URL.Query().Get(key)
	if value == "" {
		return false, nil
	}
	return strconv.ParseBool(value)
}

type statusWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *statusWriter) WriteHeader(code int) {
	w.wrote = true
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return os.ErrExist
		}
		return os.Remove(path)
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func drainAndClose(rc io.ReadCloser) {
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}
