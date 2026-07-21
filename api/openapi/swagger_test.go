package openapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	handler := Handler()
	for path, want := range map[string]string{
		"/":             "SwaggerUIBundle",
		"/openapi.yaml": "FloatLab Management API",
		"/stacks.yaml":  "/stacks",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 || !strings.Contains(response.Body.String(), want) {
			t.Errorf("GET %s: status %d, body %q", path, response.Code, response.Body.String())
		}
	}
}
