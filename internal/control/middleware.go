package control

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"
)

type contextKey string

const (
	keyRemoteUser  contextKey = "remote-user"
	keyRemoteEmail contextKey = "remote-email"
	keyRemoteName  contextKey = "remote-name"
)

// pangolinIdentity reads identity headers injected by the Pangolin OIDC proxy.
// No auth is performed in the application layer — Pangolin is the sole gate.
func pangolinIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		ctx = context.WithValue(ctx, keyRemoteUser, r.Header.Get("Remote-User"))
		ctx = context.WithValue(ctx, keyRemoteEmail, r.Header.Get("Remote-Email"))
		ctx = context.WithValue(ctx, keyRemoteName, r.Header.Get("Remote-Name"))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestLogger(log *zap.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(ww, r)
			log.Info("http",
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.status),
				zap.Duration("duration", time.Since(start)),
			)
		})
	}
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}
