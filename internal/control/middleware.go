package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

type contextKey string

const (
	keyRemoteUser  contextKey = "remote-user"
	keyRemoteEmail contextKey = "remote-email"
	keyRemoteName  contextKey = "remote-name"
	keyActor       contextKey = "actor"
	keyOperationID contextKey = "operation-id"
)

type adminClaims struct {
	Roles []string `json:"roles"`
	jwt.RegisteredClaims
}

func (s *Server) requireAdminJWT(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if s.cfg.JWTSecret == "" {
			writeError(w, http.StatusServiceUnavailable, "JWT authentication is not configured")
			return
		}
		if !strings.HasPrefix(header, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		claims := &adminClaims{}
		options := []jwt.ParserOption{jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()})}
		if s.cfg.JWTIssuer != "" {
			options = append(options, jwt.WithIssuer(s.cfg.JWTIssuer))
		}
		if s.cfg.JWTAudience != "" {
			options = append(options, jwt.WithAudience(s.cfg.JWTAudience))
		}
		token, err := jwt.ParseWithClaims(strings.TrimPrefix(header, "Bearer "), claims,
			func(*jwt.Token) (interface{}, error) { return []byte(s.cfg.JWTSecret), nil }, options...)
		if err != nil || !token.Valid || claims.Subject == "" || !contains(claims.Roles, "admin") {
			writeError(w, http.StatusUnauthorized, "invalid administrator token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyActor, claims.Subject)))
	})
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (s *Server) idempotency(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 255 {
			writeError(w, http.StatusBadRequest, "Idempotency-Key is required and must not exceed 255 characters")
			return
		}
		requestDir := ""
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-tar") {
			requestDir = "/floatlab"
			if err := os.MkdirAll(requestDir, 0755); err != nil {
				writeError(w, http.StatusServiceUnavailable, "cannot stage project archive")
				return
			}
		}
		bodyFile, err := os.CreateTemp(requestDir, ".floatlab-request-*")
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "cannot stage request")
			return
		}
		defer os.Remove(bodyFile.Name())
		defer bodyFile.Close()
		digest := sha256.New()
		_, _ = io.WriteString(digest, r.URL.RawQuery+"\x00")
		if _, err := io.Copy(io.MultiWriter(bodyFile, digest), r.Body); err != nil {
			writeError(w, http.StatusBadRequest, "cannot read request body")
			return
		}
		if _, err := bodyFile.Seek(0, io.SeekStart); err != nil {
			writeError(w, http.StatusServiceUnavailable, "cannot stage request")
			return
		}
		r.Body = bodyFile
		hash := hex.EncodeToString(digest.Sum(nil))
		actor, _ := r.Context().Value(keyActor).(string)

		existing, err := s.db.Query(r.Context(), rqlite.Statement{
			SQL:    `SELECT method, path, request_hash, operation_id, status, response FROM idempotency_keys WHERE actor=? AND key=?`,
			Params: []interface{}{actor, key},
		})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, err.Error())
			return
		}
		if len(existing.Values) > 0 {
			row := existing.Values[0]
			method, _ := row[0].(string)
			path, _ := row[1].(string)
			oldHash, _ := row[2].(string)
			if method != r.Method || path != r.URL.Path || oldHash != hash {
				writeError(w, http.StatusConflict, "idempotency key was used for a different request")
				return
			}
			status := number(row[4])
			if status == 0 {
				status = http.StatusAccepted
			}
			if response, ok := row[5].(string); ok && response != "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, response)
				return
			}
			opID, _ := row[3].(string)
			writeJSON(w, status, map[string]string{"operation_id": opID, "status": "running"})
			return
		}

		now := time.Now().UTC()
		payload := ""
		if strings.Contains(r.Header.Get("Content-Type"), "json") || strings.Contains(r.Header.Get("Content-Type"), "yaml") {
			if bytes, err := io.ReadAll(bodyFile); err == nil {
				payload = string(bytes)
				_, _ = bodyFile.Seek(0, io.SeekStart)
			}
		}
		op := operation.Operation{ID: uuid.NewString(), StackID: stackIDFromPath(r.URL.Path), Action: actionFromRequest(r), Actor: actor, State: "pending", Checkpoint: "accepted", Payload: payload, CreatedAt: now, UpdatedAt: now}
		if err := s.db.Execute(r.Context(), []rqlite.Statement{
			{SQL: `INSERT INTO operations(id, stack_id, action, actor, state, checkpoint, payload, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, Params: []interface{}{op.ID, op.StackID, op.Action, op.Actor, op.State, op.Checkpoint, op.Payload, now, now}},
			{SQL: `INSERT INTO idempotency_keys(actor,key,method,path,request_hash,operation_id,created_at) VALUES(?,?,?,?,?,?,?)`, Params: []interface{}{actor, key, r.Method, r.URL.Path, hash, op.ID, now}},
		}); err != nil {
			writeError(w, http.StatusConflict, "idempotency key already in use")
			return
		}

		recorder := newBufferedResponse()
		next.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), keyOperationID, op.ID)))
		state := "succeeded"
		if recorder.status == http.StatusAccepted {
			state = "running"
		} else if recorder.status >= 400 {
			state = "failed"
		}
		response := recorder.body.Bytes()
		if recorder.status == http.StatusAccepted {
			var value map[string]interface{}
			if json.Unmarshal(response, &value) == nil {
				value["operation_id"] = op.ID
				response, _ = json.Marshal(value)
				response = append(response, '\n')
			}
		}
		_ = s.db.Execute(context.Background(), []rqlite.Statement{
			{SQL: `UPDATE operations SET state=?, checkpoint=?, updated_at=? WHERE id=?`, Params: []interface{}{state, state, time.Now().UTC(), op.ID}},
			{SQL: `UPDATE idempotency_keys SET status=?, response=? WHERE actor=? AND key=?`, Params: []interface{}{recorder.status, string(response), actor, key}},
		})
		for name, values := range recorder.header {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		if recorder.status == http.StatusAccepted {
			w.Header().Set("Location", "/api/v1/operations/"+op.ID)
		}
		w.WriteHeader(recorder.status)
		_, _ = w.Write(response)
	})
}

type bufferedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func newBufferedResponse() *bufferedResponse {
	return &bufferedResponse{header: make(http.Header), status: http.StatusOK}
}
func (w *bufferedResponse) Header() http.Header         { return w.header }
func (w *bufferedResponse) WriteHeader(status int)      { w.status = status }
func (w *bufferedResponse) Write(p []byte) (int, error) { return w.body.Write(p) }

func number(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
func actionFromRequest(r *http.Request) string {
	action := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/stacks"), "/")
	if action == "" {
		return "create"
	}
	parts := strings.Split(action, "/")
	if r.Method == http.MethodDelete {
		return "delete"
	}
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return strings.ToLower(r.Method)
}
func stackIDFromPath(path string) string {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/stacks/"), "/")
	if len(parts) > 0 && parts[0] != path {
		return parts[0]
	}
	return ""
}
func operationID(ctx context.Context) string { id, _ := ctx.Value(keyOperationID).(string); return id }

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

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *responseWriter) WriteHeader(status int) {
	rw.status = status
	rw.ResponseWriter.WriteHeader(status)
}
