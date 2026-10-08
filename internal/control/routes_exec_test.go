package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestExecRequiresAdminJWT(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "test-secret"}}
	router := chi.NewRouter()
	registerStackRoutes(router, s)
	request := httptest.NewRequest(http.MethodPost, "/stacks/stack/containers/container/exec", strings.NewReader(`{"command":["echo"]}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestExecDispatchesAndRejectsInvalidArguments(t *testing.T) {
	database := testDatabase(t, stackRow("stack-1", "demo", "node-a", "node-b"), nil)
	defer database.Close()
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	server.Handle("docker.exec.run", func(_ context.Context, raw json.RawMessage) (any, error) {
		var payload ipc.ExecPayload
		json.Unmarshal(raw, &payload)
		if payload.StackID != "stack-1" || payload.ContainerID != "container-1" || strings.Join(payload.Command, ",") != "echo,a b" {
			t.Errorf("payload=%+v", payload)
		}
		return ipc.ExecResult{Stdout: "out", Stderr: "err", ExitCode: 7}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Run(ctx)
	waitForSocket(t, socket)
	hosts := hostclient.NewPool(zap.NewNop())
	defer hosts.Close()
	hosts.Register("node-a", socket)
	s := &Server{store: config.NewStore(rqlite.NewClient(database.URL)), raft: testRaftNode(t), hosts: hosts}
	for _, body := range []string{`{"command":["echo","a b"]}`, `{"command":[]}`, `{"command":"echo"}`} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		route := chi.NewRouteContext()
		route.URLParams.Add("id", "stack-1")
		route.URLParams.Add("containerId", "container-1")
		request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
		response := httptest.NewRecorder()
		s.handleContainerExec(response, request)
		if strings.Contains(body, "a b") {
			if response.Code != 200 || !strings.Contains(response.Body.String(), `"exit_code":7`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		} else if response.Code != 400 {
			t.Fatalf("invalid command status=%d", response.Code)
		}
	}
}

func TestAllocationsIncludeManagedIPv4(t *testing.T) {
	database := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeTestJSON(t, w, map[string]any{"results": []any{map[string]any{"values": [][]any{{"allocation", "stack", "", "10.254.240.2", "", "2026-10-07T00:00:00Z", "pool", "active"}}}}})
	}))
	defer database.Close()
	s := &Server{db: rqlite.NewClient(database.URL)}
	response := httptest.NewRecorder()
	s.handleListAllocations(response, httptest.NewRequest("GET", "/network/allocations?stack_id=stack", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"pool_id":"pool"`) || !strings.Contains(response.Body.String(), `"state":"active"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
