package control

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestServerServesUIAlongsideAPI(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "assets"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"index.html":    "<!doctype html><div id=\"app\"></div>",
		"assets/app.js": "console.log('FloatLab')",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{cfg: &Config{UIDir: dir, JWTSecret: "ui-test"}, log: zap.NewNop(), auth: newAuthLimiter()}
	router := s.buildRouter()
	for _, tc := range []struct {
		method, path      string
		status            int
		body, contentType string
	}{
		{http.MethodGet, "/", 200, "<div id=\"app\"></div>", "text/html"},
		{http.MethodGet, "/assets/app.js", 200, "console.log('FloatLab')", "javascript"},
		{http.MethodHead, "/", 200, "", "text/html"},
		{http.MethodGet, "/assets/missing.js", 404, "", ""},
		{http.MethodGet, "/api/v1/missing", 404, "", ""},
		{http.MethodGet, "/api/v1/nodes", 401, "bearer token required", "application/json"},
		{http.MethodGet, "/swagger", 301, "", ""},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.body) || !strings.Contains(response.Header().Get("Content-Type"), tc.contentType) {
				t.Fatalf("got %d %s %q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD response contains a body")
			}
		})
	}
}
