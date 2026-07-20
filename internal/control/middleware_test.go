package control

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestRequireAdminJWT(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "test-secret", JWTIssuer: "floatlab", JWTAudience: "management"}}
	handler := s.requireAdminJWT(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if actor, _ := r.Context().Value(keyActor).(string); actor != "admin-user" {
			t.Fatalf("actor = %q", actor)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := signedToken(t, "test-secret", []string{"admin"})
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+valid)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid token status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"viewer"}))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("viewer token status = %d", response.Code)
	}
}

func signedToken(t *testing.T, secret string, roles []string) string {
	t.Helper()
	claims := adminClaims{Roles: roles, RegisteredClaims: jwt.RegisteredClaims{
		Subject:   "admin-user",
		Issuer:    "floatlab",
		Audience:  jwt.ClaimStrings{"management"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return token
}
