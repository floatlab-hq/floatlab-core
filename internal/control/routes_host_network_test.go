package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestHostNetworkRecoveryWithoutDatabase(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	daemon.Handle("net.settings.get", func(context.Context, json.RawMessage) (any, error) {
		return hostnetwork.Status{Config: hostnetwork.DefaultConfig(), Revision: "revision"}, nil
	})
	daemon.Handle("net.settings.retry", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p hostnetwork.ApplyPayload
		_ = json.Unmarshal(raw, &p)
		if p.Key == "accepted-key" {
			return hostnetwork.Change{ID: "accepted", State: "confirmed", Candidate: p.Config}, nil
		}
		return nil, nil
	})
	daemon.Handle("net.settings.apply", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p hostnetwork.ApplyPayload
		_ = json.Unmarshal(raw, &p)
		if p.Key != "retry-key" || p.Actor != "admin-user" {
			return nil, hostnetwork.Error("invalid", "missing retry identity")
		}
		return hostnetwork.Change{ID: "change", State: "applying"}, nil
	})
	daemon.Handle("net.settings.confirm", func(context.Context, json.RawMessage) (any, error) {
		return nil, hostnetwork.Error("conflict", "confirmation deadline expired")
	})
	daemon.Handle("net.settings.rollback", func(context.Context, json.RawMessage) (any, error) {
		return hostnetwork.Change{ID: "change", State: "rolled_back"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	hosts.Register("node", socket)
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := hosts.Execute(ctx, "node", "net.settings.get", nil); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("IPC did not start")
		}
		time.Sleep(time.Millisecond)
	}
	s := &Server{cfg: &Config{JWTSecret: "test-secret", JWTIssuer: "floatlab", JWTAudience: "management"}, hosts: hosts}
	router := chi.NewRouter()
	registerHostNetworkRoutes(router, s)
	token := signedToken(t, "test-secret", []string{"admin"})
	for _, tc := range []struct {
		method, path, body string
		authenticated      bool
		want               int
	}{{"GET", "/nodes/node/settings/network", "", false, 401}, {"GET", "/nodes/node/settings/network", "", true, 200}, {"PUT", "/nodes/node/settings/network", `{"revision":"revision","config":{"version":1,"excluded_macs":[],"bonds":[],"ipv4":{"mode":"dhcp"},"dns_servers":[]}}`, true, 202}, {"PUT", "/nodes/node/settings/network", `{"revision":"revision","config":{"version":1,"excluded_macs":[],"bonds":[],"ipv4":{"mode":"dhcp"},"dns_servers":[],"default_pool_id":"existing"}}`, true, 202},
		{"POST", "/nodes/node/settings/network/changes/change/confirm", "", true, 409}, {"POST", "/nodes/node/settings/network/changes/change/rollback", "", true, 200}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.authenticated {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Idempotency-Key", "retry-key")
		if strings.Contains(tc.body, `"default_pool_id":"existing"`) {
			req.Header.Set("Idempotency-Key", "accepted-key")
		}
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != tc.want {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, res.Code, res.Body.String())
		}
		if tc.want == http.StatusConflict && !strings.Contains(res.Body.String(), `"code":"network.conflict"`) {
			t.Fatal("typed IPC conflict was lost", res.Body.String())
		}
	}
	cancel()
	<-done
}
