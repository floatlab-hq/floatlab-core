package control

import (
	"github.com/floatlab/floatlab-core/api/openapi"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagementRouteContract(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "contract-test"}, log: zap.NewNop(), auth: newAuthLimiter()}
	router := s.buildRouter()
	expected := map[string]bool{}
	operations, err := openapi.ManagementOperations()
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range operations {
		expected[op.Method+" "+op.Path] = true
	}
	err = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") {
			return nil
		}
		key := method + " " + route
		if !expected[key] {
			t.Errorf("undocumented registered operation: %s", key)
		}
		delete(expected, key)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range expected {
		t.Errorf("documented operation has no route: %s", key)
	}
	for _, op := range operations {
		if op.Public {
			continue
		}
		t.Run(op.ID+"/unauthenticated", func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(op.Method, op.Path, strings.NewReader(`{}`)))
			if response.Code != 401 {
				t.Fatalf("%s %s without token: %d %s", op.Method, op.Path, response.Code, response.Body.String())
			}
		})
	}
}
