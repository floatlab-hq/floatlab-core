package control

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestUpdateRoutesWithoutDatabase(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "update.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	requests := make(chan map[string]string, 8)
	daemon.Handle("management.update.start", func(_ context.Context, raw json.RawMessage) (any, error) {
		var payload map[string]string
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		requests <- payload
		return map[string]string{"operation_id": payload["operation_id"], "state": "running", "phase": "prepare"}, nil
	})
	daemon.Handle("host.update.status", func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	daemon.Handle("host.update.commit", func(context.Context, json.RawMessage) (any, error) {
		return nil, &ipc.RPCError{Code: "update.conflict", Message: "trial not ready"}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	defer func() { cancel(); <-done }()
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	hosts.Register("local", socket)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := hosts.Execute(ctx, "local", "host.update.status", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC did not start")
		}
		time.Sleep(time.Millisecond)
	}
	s := &Server{cfg: &Config{JWTSecret: "test-secret", JWTIssuer: "floatlab", JWTAudience: "management"}, hosts: hosts}
	router := chi.NewRouter()
	registerUpdateRoutes(router, s)
	token := signedToken(t, "test-secret", []string{"admin"})
	for _, tc := range []struct {
		path, method, body, key string
		auth                    bool
		want                    int
	}{
		{"management/start", "POST", `{"release":"manage-v1.2.3"}`, "retry", false, 401},
		{"management/start", "POST", `{"release":"manage-v1.2.3"}`, "", true, 400},
		{"management/start", "POST", `{"release":"host-v1.2.3"}`, "retry", true, 400},
		{"management/start", "POST", `{"release":"manage-vlatest"}`, "retry", true, 400},
		{"management/start", "POST", `{"release":"manage-v1.2.3","actor":"other"}`, "retry", true, 400},
		{"management/start", "POST", `{"release":"manage-v1.2.3"} {}`, "retry", true, 400},
		{"management/start", "POST", `{"release":"manage-v1.2.3","operation_id":"../../other"}`, "retry", true, 400},
		{"management/start", "POST", `{"release":"manage-v1.2.3"}`, "retry", true, 202},
		{"management/start", "POST", `{"release":"manage-v1.2.3"}`, strings.Repeat("key/", 30), true, 202},
		{"management/commit", "POST", `{}`, "retry", true, 404},
		{"unknown/start", "POST", `{}`, "retry", true, 404},
		{"host/status", "POST", `{}`, "retry", true, 404},
		{"host/commit", "POST", `{"operation_id":"trial"}`, "commit-retry", true, 409},
		{"host/status", "GET", "", "", true, 200},
	} {
		req := httptest.NewRequest(tc.method, "/nodes/local/updates/"+tc.path, strings.NewReader(tc.body))
		if tc.auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Idempotency-Key", tc.key)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
	select {
	case payload := <-requests:
		if payload["actor"] != "admin-user" || payload["operation_id"] != "retry" || payload["idempotency_key"] != "retry" {
			t.Fatalf("lost update identity: %#v", payload)
		}
	default:
		t.Fatal("valid start did not reach hostd")
	}
	select {
	case payload := <-requests:
		if len(payload["operation_id"]) != 64 || payload["idempotency_key"] != strings.Repeat("key/", 30) {
			t.Fatalf("unsafe key was not converted to a safe transaction identity: %#v", payload)
		}
	default:
		t.Fatal("long idempotency key did not reach hostd")
	}
	if len(requests) != 0 {
		t.Fatal("rejected requests reached hostd")
	}
}
