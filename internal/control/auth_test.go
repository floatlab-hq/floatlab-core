package control

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestLoginIssuesAdminJWT(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("floatlab"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{
			"values": [][]interface{}{{string(hash), `["admin"]`}},
		}}})
	}))
	defer database.Close()

	s := &Server{cfg: &Config{JWTSecret: "secret", JWTIssuer: "floatlab", JWTAudience: "management"}, db: rqlite.NewClient(database.URL)}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/token", strings.NewReader(`{"username":"demo","password":"floatlab"}`))
	response := httptest.NewRecorder()
	s.handleLogin(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response must not be cached")
	}
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	claims := &adminClaims{}
	token, err := jwt.ParseWithClaims(body.AccessToken, claims, func(*jwt.Token) (interface{}, error) { return []byte("secret"), nil }, jwt.WithIssuer("floatlab"), jwt.WithAudience("management"))
	if err != nil || !token.Valid || claims.Subject != "demo" || !contains(claims.Roles, "admin") {
		t.Fatalf("issued token = %#v, claims = %#v, error = %v", token, claims, err)
	}
}

func TestBootstrapUserStoresHash(t *testing.T) {
	var payload string
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		payload = string(body)
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{}}})
	}))
	defer database.Close()

	if err := BootstrapUser(context.Background(), rqlite.NewClient(database.URL), "demo", "floatlab"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, `"floatlab"`) || !strings.Contains(payload, `"$2`) {
		t.Fatalf("bootstrap payload does not contain only a bcrypt hash: %s", payload)
	}
}

func TestAuthRateLimit(t *testing.T) {
	s := &Server{auth: newAuthLimiter()}
	handler := s.limitAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for attempt := 1; attempt <= 6; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "/auth/token", nil)
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		want := http.StatusNoContent
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.Code != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.Code, want)
		}
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}
