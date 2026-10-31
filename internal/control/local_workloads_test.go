package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/orchestrator"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/notify"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func TestLocalWorkloadCreateStartStopWithoutLabDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), "node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := floatraft.NewLocalNode(ctx, db, "node-a", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	var remoteCalls atomic.Int64
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { remoteCalls.Add(1); http.Error(w, "no quorum", 503) }))
	defer unavailable.Close()
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	var starts, stops atomic.Int64
	for _, method := range []string{"fs.dataset.create", "compose.source.write", "compose.up", "compose.down"} {
		method := method
		daemon.Handle(method, func(context.Context, json.RawMessage) (any, error) {
			if method == "compose.up" {
				starts.Add(1)
			}
			if method == "compose.down" {
				stops.Add(1)
			}
			return map[string]bool{"ok": true}, nil
		})
	}
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemon.Run(ctx) }()
	waitForSocket(t, socket)
	hosts := hostclient.NewPool(zap.NewNop())
	hosts.Register("node-a", socket)
	store := config.NewStore(db)
	server := NewServer(&Config{LocalNodeID: "node-a", LabDB: rqlite.NewClient(unavailable.URL)}, db, store, lifecycle, hosts, notify.NewBroker(), nil, zap.NewNop())
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"offline-web","compose_file":`+jsonString(validStackCompose)+`}`))
	response := httptest.NewRecorder()
	server.handleCreateStack(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	stacks, err := store.ListStacks(ctx)
	if err != nil || len(stacks) != 1 {
		t.Fatalf("local stacks: %v %v", stacks, err)
	}
	id := stacks[0].ID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err = db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO operations(id,stack_id,action,actor,created_at,updated_at) VALUES('create',?,'create','test',?,?)`, Params: []any{id, now, now}}}); err != nil {
		t.Fatal(err)
	}
	orch := orchestrator.New(store, lifecycle, hosts, db, zap.NewNop())
	orchDone := make(chan error, 1)
	go func() { orchDone <- orch.Run(ctx) }()
	waitState := func(want run.State) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if state, ok := lifecycle.FSM().State(id); ok && state.State == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		state, _ := lifecycle.FSM().State(id)
		t.Fatalf("wanted %s, got %+v", want, state)
	}
	waitState(run.StateIdle)
	routeRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		route := chi.NewRouteContext()
		route.URLParams.Add("id", id)
		return req.WithContext(context.WithValue(ctx, chi.RouteCtxKey, route))
	}
	response = httptest.NewRecorder()
	server.handleStartStack(response, routeRequest())
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	waitState(run.StateRunningPrimary)
	response = httptest.NewRecorder()
	server.handleStopStack(response, routeRequest())
	if response.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", response.Code, response.Body.String())
	}
	waitState(run.StateIdle)
	if starts.Load() != 1 || stops.Load() != 1 {
		t.Fatalf("host execution start=%d stop=%d", starts.Load(), stops.Load())
	}
	if remoteCalls.Load() != 0 {
		t.Fatalf("local lifecycle contacted lab consensus %d times", remoteCalls.Load())
	}
	cancel()
	<-orchDone
	<-daemonDone
}
