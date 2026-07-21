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

func TestManagementSpecUsesCurrentHost(t *testing.T) {
	response := httptest.NewRecorder()
	Handler().ServeHTTP(response, httptest.NewRequest("GET", "/openapi.yaml", nil))

	body := response.Body.String()
	if !strings.Contains(body, "url: /api/v1") || strings.Contains(body, "floatlab-node") {
		t.Fatalf("management API server must be relative to the current host: %q", body)
	}
	if !strings.Contains(body, "securitySchemes:") || !strings.Contains(body, "scheme: bearer") {
		t.Fatalf("management API must declare bearer authentication: %q", body)
	}
}
