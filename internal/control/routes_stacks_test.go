package control

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

const validStackCompose = `x-fl-stack:
  primary_node: node-a
  secondary_node: node-b
  storage:
    pool: floatlab
  failover:
    mode: auto
    auto_trigger_after: 30s
services: {}`

func TestValidateStackRouteNeedsJWTButNotIdempotencyKey(t *testing.T) {
	s := &Server{cfg: &Config{JWTSecret: "test-secret", JWTIssuer: "floatlab", JWTAudience: "management"}}
	router := chi.NewRouter()
	registerStackRoutes(router, s)

	request := httptest.NewRequest(http.MethodPost, "/stacks/validate", strings.NewReader(`{"name":"demo","compose_file":`+jsonString(validStackCompose)+`}`))
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("validate status = %d, body = %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/stacks/validate", strings.NewReader(`{"name":"demo","compose_file":`+jsonString(validStackCompose)+`}`))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated validate status = %d", response.Code)
	}
}

func TestValidateStackRejectsEditNodeChange(t *testing.T) {
	database := testDatabase(t, stackRow("stack-1", "demo", "node-a", "node-b"), nil)
	defer database.Close()
	s := &Server{store: config.NewStore(rqlite.NewClient(database.URL))}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"stack_id":"stack-1","compose_file":`+jsonString(strings.Replace(validStackCompose, "node-a", "node-c", 1))+`}`))
	response := httptest.NewRecorder()
	s.handleValidateStack(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"compose_validation"`) {
		t.Fatalf("changed nodes status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateStackRejectsLegacyMismatch(t *testing.T) {
	s := &Server{}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"demo","primary_node":"other-node","compose_file":`+jsonString(validStackCompose)+`}`))
	response := httptest.NewRecorder()
	s.handleCreateStack(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "node assignments must match") {
		t.Fatalf("legacy mismatch status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestCreateStackDerivesCanonicalConfiguration(t *testing.T) {
	var executePayload string
	database := testDatabase(t, nil, &executePayload)
	defer database.Close()
	raftNode := testRaftNode(t)
	s := &Server{store: config.NewStore(rqlite.NewClient(database.URL)), raft: raftNode}
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"demo","compose_file":`+jsonString(validStackCompose)+`}`))
	response := httptest.NewRecorder()
	s.handleCreateStack(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, wanted := range []string{"name: demo", "node-a", "node-b", "floatlab/demo", "auto", "30s"} {
		if !strings.Contains(executePayload, wanted) {
			t.Fatalf("stored stack is missing %q: %s", wanted, executePayload)
		}
	}
}

func TestContainerStartChecksStateAndDispatchesOwnedContainer(t *testing.T) {
	database := testDatabase(t, stackRow("stack-1", "demo", "node-a", "node-b"), nil)
	defer database.Close()
	raftNode := testRaftNode(t)
	if err := raftNode.Apply(run.StackStateChanged{StackID: "stack-1", From: run.StateIdle, To: run.StateRunningPrimary, Event: run.EventStartStack, Timestamp: time.Now().UTC(), NodeID: "node-a"}, time.Second); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	server := ipc.NewServer(socket, zap.NewNop())
	server.Handle("docker.list", func(context.Context, json.RawMessage) (any, error) {
		return ipc.DockerListResult{Containers: []ipc.ContainerInfo{{ID: "container-1", Name: "web", Image: "nginx", State: "stopped", StackID: "stack-1"}}}, nil
	})
	var called atomic.Bool
	server.Handle("docker.start", func(_ context.Context, raw json.RawMessage) (any, error) {
		var payload ipc.DockerContainerPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil, err
		}
		if payload.StackID != "stack-1" || payload.ContainerID != "container-1" {
			t.Fatalf("unexpected start payload: %#v", payload)
		}
		called.Store(true)
		return ipc.DockerContainerResult{Container: ipc.ContainerInfo{ID: "container-1", Name: "web", Image: "nginx", State: "running", Service: "web", StackID: "stack-1"}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = server.Run(ctx) }()
	waitForSocket(t, socket)
	hosts := hostclient.NewPool(zap.NewNop())
	hosts.Register("node-a", socket)
	s := &Server{store: config.NewStore(rqlite.NewClient(database.URL)), raft: raftNode, hosts: hosts}
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request = request.WithContext(context.Background())
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "stack-1")
	rctx.URLParams.Add("containerId", "web")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, rctx))
	response := httptest.NewRecorder()
	s.handleStartStackContainer(response, request)
	if response.Code != http.StatusOK || !called.Load() || !strings.Contains(response.Body.String(), `"status":"running"`) {
		t.Fatalf("container start status = %d, called = %t, body = %s", response.Code, called.Load(), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"service":"web"`) {
		t.Fatalf("container response must include service: %s", response.Body.String())
	}
}

func TestContainerStatusMatchesPublicEnum(t *testing.T) {
	for state, want := range map[string]string{"exited": "stopped", "created": "stopped", "restarting": "error", "running": "running"} {
		if got := containerStatus(state); got != want {
			t.Errorf("containerStatus(%q) = %q, want %q", state, got, want)
		}
	}
}

func jsonString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func testDatabase(t *testing.T, row []interface{}, executePayload *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/db/query") {
			values := [][]interface{}{}
			if row != nil {
				values = append(values, row)
			}
			writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{"values": values}}})
			return
		}
		if executePayload != nil {
			data, _ := io.ReadAll(r.Body)
			*executePayload += string(data)
		}
		writeTestJSON(t, w, map[string]interface{}{"results": []interface{}{map[string]interface{}{}}})
	}))
}

func stackRow(id, name, primary, secondary string) []interface{} {
	now := time.Now().UTC().Format(time.RFC3339)
	return []interface{}{id, name, "", primary, secondary, validStackCompose, "floatlab/" + name, "", "", "", "", "auto", "30s", now, now}
}

func testRaftNode(t *testing.T) *floatraft.Node {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	node, err := floatraft.NewNode(floatraft.Config{NodeID: "test", BindAddr: address, AdvertiseAddr: address, DataDir: t.TempDir(), Bootstrap: true}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Shutdown() })
	deadline := time.Now().Add(3 * time.Second)
	for !node.IsLeader() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !node.IsLeader() {
		t.Fatal("test raft node did not become leader")
	}
	return node
}

func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("IPC socket was not created: %s", socket)
}

func TestStackStateResponseUsesDocumentedField(t *testing.T) {
	node := testRaftNode(t)
	if err := node.Apply(run.StackStateChanged{StackID: "stack-state", From: run.StateIdle, To: run.StateRunningPrimary, Event: run.EventStartStack, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	s := &Server{raft: node}
	request := httptest.NewRequest("GET", "/api/v1/stacks/stack-state/state", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("id", "stack-state")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	s.handleGetStackState(response, request)
	var body map[string]interface{}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || body["state"] != "RunningPrimary" || body["ID"] != "stack-state" {
		t.Fatalf("state response: %d %s", response.Code, response.Body.String())
	}
}
