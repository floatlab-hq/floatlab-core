package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

const tokenTTL = 24 * time.Hour

type loginRequest struct {
	Username     string `json:"username"`
	Password     string `json:"password"`
	TOTPCode     string `json:"totp_code,omitempty"`
	RecoveryCode string `json:"recovery_code,omitempty"`
}

type authAttempt struct {
	window time.Time
	count  int
}

type authLimiter struct {
	mu       sync.Mutex
	attempts map[string]authAttempt
}

func newAuthLimiter() *authLimiter { return &authLimiter{attempts: make(map[string]authAttempt)} }

func registerAuthRoutes(r chi.Router, s *Server) {
	r.With(s.limitAuth).Post("/auth/token", s.handleLogin)
}

// BootstrapUser creates the initial administrator without retaining its plaintext password.
func BootstrapUser(ctx context.Context, db *rqlite.Client, username, password string) error {
	if !validCredentials(username, password) {
		return fmt.Errorf("bootstrap username must be 1-64 characters and password 1-72 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash bootstrap password: %w", err)
	}
	now := time.Now().UTC()
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT OR IGNORE INTO users(username,password_hash,roles,created_at,updated_at) VALUES(?,?,'["admin"]',?,?)`,
		Params: []interface{}{username, string(hash), now, now},
	}})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.cfg.JWTSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "JWT authentication is not configured")
		return
	}

	var request loginRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF || !validCredentials(request.Username, request.Password) {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL:    `SELECT u.password_hash,u.roles,c.totp_secret,c.last_totp_step FROM users u LEFT JOIN user_credentials c ON c.username=u.username WHERE u.username=?`,
		Params: []interface{}{request.Username},
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "authentication unavailable")
		return
	}
	if len(result.Values) == 0 || len(result.Values[0]) < 2 {
		_, _ = bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	hash, _ := result.Values[0][0].(string)
	rolesJSON, _ := result.Values[0][1].(string)
	var roles []string
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil || json.Unmarshal([]byte(rolesJSON), &roles) != nil {
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	encrypted := ""
	if len(result.Values[0]) >= 4 {
		encrypted, _ = result.Values[0][2].(string)
	}
	if encrypted != "" {
		if request.TOTPCode == "" && request.RecoveryCode == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"error": "second factor required", "second_factor_required": true})
			return
		}
		ok, err := s.verifySecondFactor(r.Context(), request, encrypted, result.Values[0][3])
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authentication unavailable")
			return
		}
		if !ok {
			writeError(w, http.StatusUnauthorized, "invalid credentials or second factor")
			return
		}
	}

	now := time.Now().UTC()
	claims := adminClaims{Roles: roles, RegisteredClaims: jwt.RegisteredClaims{
		Subject:   request.Username,
		Issuer:    s.cfg.JWTIssuer,
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenTTL)),
		IssuedAt:  jwt.NewNumericDate(now),
	}}
	if s.cfg.JWTAudience != "" {
		claims.Audience = jwt.ClaimStrings{s.cfg.JWTAudience}
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token": token,
		"token_type":   "Bearer",
		"expires_in":   int(tokenTTL.Seconds()),
	})
}

func validCredentials(username, password string) bool {
	return username != "" && utf8.RuneCountInString(username) <= 64 && password != "" && len(password) <= 72
}

func (s *Server) limitAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.allow(clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "too many login attempts")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *authLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	// ponytail: process-local fixed windows are enough for one control plane; use a shared limiter when replicas accept logins.
	for key, attempt := range l.attempts {
		if now.Sub(attempt.window) >= 10*time.Minute {
			delete(l.attempts, key)
		}
	}
	attempt := l.attempts[ip]
	if attempt.window.IsZero() || now.Sub(attempt.window) >= time.Minute {
		attempt = authAttempt{window: now}
	}
	if attempt.count >= 5 {
		return false
	}
	attempt.count++
	l.attempts[ip] = attempt
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) verifySecondFactor(ctx context.Context, request loginRequest, encrypted string, lastValue interface{}) (bool, error) {
	if request.TOTPCode != "" && request.RecoveryCode != "" {
		return false, nil
	}
	if request.RecoveryCode != "" {
		result, err := s.db.Request(ctx, rqlite.Statement{SQL: `DELETE FROM user_recovery_codes WHERE username=? AND code_hash=?`, Params: []interface{}{request.Username, credentials.RecoveryHash(request.RecoveryCode)}})
		if err != nil {
			return false, err
		}
		return result.RowsAffected == 1, nil
	}
	secret, err := credentials.Decrypt(s.cfg.CredentialKey, encrypted)
	if err != nil {
		return false, err
	}
	last, ok := lastValue.(float64)
	if !ok {
		return false, fmt.Errorf("invalid credential state")
	}
	step, ok := credentials.Verify(secret, request.TOTPCode, time.Now(), int64(last))
	if !ok {
		return false, nil
	}
	result, err := s.db.Request(ctx, rqlite.Statement{SQL: `UPDATE user_credentials SET last_totp_step=? WHERE username=? AND last_totp_step<?`, Params: []interface{}{step, request.Username, step}})
	if err != nil {
		return false, err
	}
	return result.RowsAffected == 1, nil
}
