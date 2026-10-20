package control

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

var activeManagementMutations atomic.Int64

func updateMutationFence(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		directory := os.Getenv("FLOATLAB_ACTIVATION_DIR")
		preflight := os.Getenv("FLOATLAB_PREFLIGHT") == "true"
		read := r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions
		// Login and hostd update commands remain available during maintenance.
		recovery := r.URL.Path == "/api/v1/auth/token" || strings.HasPrefix(r.URL.Path, "/api/v1/nodes/") && strings.Contains(r.URL.Path, "/updates/")
		if !read && !recovery {
			activeManagementMutations.Add(1)
			defer activeManagementMutations.Add(-1)
		}
		if preflight && !read && r.URL.Path != "/api/v1/auth/token" {
			writeErrorCode(w, http.StatusServiceUnavailable, "update.preflight", "Preflight management does not accept mutations")
			return
		}
		if directory != "" && !read && !recovery {
			_, err := os.Stat(filepath.Join(directory, "blocked"))
			if err == nil || !os.IsNotExist(err) {
				writeErrorCode(w, http.StatusServiceUnavailable, "update.maintenance", "Management mutations are paused for an appliance update")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) runUpdateBackground(ctx context.Context) {
	runUpdateBackground(ctx, s.runAlertEvaluator)
}

// Acknowledge the API drain only after its admitted writes and evaluator stop.
func runUpdateBackground(ctx context.Context, start func(context.Context)) {
	directory := os.Getenv("FLOATLAB_ACTIVATION_DIR")
	preflight := os.Getenv("FLOATLAB_PREFLIGHT") == "true"
	var cancel context.CancelFunc
	var done chan struct{}
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		blocked := preflight
		var operation []byte
		if directory != "" {
			var err error
			operation, err = os.ReadFile(filepath.Join(directory, "blocked"))
			blocked = blocked || err == nil || !os.IsNotExist(err)
		}
		if blocked && cancel != nil {
			cancel()
			select {
			case <-done:
				cancel = nil
			case <-ctx.Done():
				return
			}
		}
		if directory != "" {
			marker := filepath.Join(directory, "api-drained")
			if blocked && len(operation) > 0 && activeManagementMutations.Load() == 0 {
				_ = os.WriteFile(marker, operation, 0600)
			} else {
				_ = os.Remove(marker)
			}
		}
		if !blocked && cancel == nil {
			var background context.Context
			background, cancel = context.WithCancel(ctx)
			done = make(chan struct{})
			go func() { defer close(done); start(background) }()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
