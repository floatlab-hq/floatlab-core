package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestStackMoveRequiresLab(t *testing.T) {
	server, _ := peerWorkloadServer(t, "node-a")
	response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/5afb34d9-7fa4-46a7-a5eb-f4c073cda364/move", `{"destination_node_id":"node-b"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "lab") {
		t.Fatalf("move outside the lab: %d %s", response.Code, response.Body.String())
	}
}

type moveTestTransport func(*http.Request) (*http.Response, error)

func (fn moveTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestStackMoveFencesSourceAndResumesAfterLostDestinationReply(t *testing.T) {
	ctx := t.Context()
	source, sourceDB := peerWorkloadServer(t, "node-a")
	destination, destinationDB := peerWorkloadServer(t, "node-b")
	source.cfg.LabDB, destination.cfg.LabDB = sourceDB, sourceDB
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	const path = "/api/v1/stacks/" + id
	const root = "floatlab/web"
	const data = root + "/data"
	const project = `name: web
services:
  init:
    image: alpine
    command: ["true"]
  web:
    image: nginx
    depends_on:
      init:
        condition: service_completed_successfully
    ports: ["8088:80"]
    volumes: ["data:/data"]
volumes:
  data: {}
`
	if err := source.store.CreateStack(ctx, &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: root, ComposeYAML: project}); err != nil {
		t.Fatal(err)
	}
	if err := source.applyMoveState(id, run.StateStarting, run.StateRunningPrimary, run.EventStartDone); err != nil {
		t.Fatal(err)
	}
	pool := ipam.Pool{Name: "destination", CIDR: "192.0.2.0/24", StartIP: "192.0.2.200", EndIP: "192.0.2.200", NodeIDs: []string{"node-b"}}
	if err := ipam.SavePool(ctx, destinationDB, &pool); err != nil {
		t.Fatal(err)
	}
	var traceMu sync.Mutex
	var trace []string
	record := func(action string) {
		traceMu.Lock()
		defer traceMu.Unlock()
		trace = append(trace, action)
	}
	var stopped atomic.Bool
	source.hosts = moveTestHosts(t, "node-a", map[string]ipc.Handler{
		"fs.dataset.list": func(context.Context, json.RawMessage) (any, error) {
			return ipc.DatasetListResult{Datasets: []ipc.DatasetInfoResult{{Name: root}, {Name: data}}}, nil
		},
		"fs.snapshot.create": func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		"fs.repl.send": func(_ context.Context, raw json.RawMessage) (any, error) {
			var send ipc.ReplSendPayload
			if err := json.Unmarshal(raw, &send); err != nil {
				return nil, err
			}
			action := "seed "
			if stopped.Load() {
				action = "final "
			}
			record(action + send.Dataset)
			return nil, nil
		},
		"compose.down": func(ctx context.Context, _ json.RawMessage) (any, error) {
			_, reserved, err := ipam.StackService(ctx, destinationDB, id)
			if err != nil || !reserved {
				return nil, fmt.Errorf("destination had no address reserved at stop: %v", err)
			}
			stopped.Store(true)
			record("stop source")
			return nil, nil
		},
		"docker.list": func(context.Context, json.RawMessage) (any, error) { return ipc.DockerListResult{}, nil },
		"fs.move.archive": func(context.Context, json.RawMessage) (any, error) {
			record("archive source")
			return nil, nil
		},
	})
	destination.hosts = moveTestHosts(t, "node-b", map[string]ipc.Handler{
		"fs.dataset.list": func(context.Context, json.RawMessage) (any, error) {
			return ipc.DatasetListResult{}, nil
		},
		"net.settings.get": func(context.Context, json.RawMessage) (any, error) {
			return json.RawMessage(`{"live":{"primary_addresses":["192.0.2.20/24"]}}`), nil
		},
		"fs.move.activate": func(ctx context.Context, _ json.RawMessage) (any, error) {
			if _, err := source.store.GetStack(ctx, id); err == nil || !stopped.Load() {
				return nil, errors.New("source was still authoritative at activation")
			}
			record("activate destination")
			return nil, nil
		},
		"compose.source.write": func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		"net.service.add":      func(context.Context, json.RawMessage) (any, error) { return nil, nil },
		"compose.up": func(_ context.Context, raw json.RawMessage) (any, error) {
			var up ipc.ComposeUpPayload
			if err := json.Unmarshal(raw, &up); err != nil {
				return nil, err
			}
			if !strings.Contains(up.ComposeFile, "192.0.2.200") {
				return nil, errors.New("missing destination address in Compose")
			}
			record("start destination")
			return nil, nil
		},
		"docker.list": func(context.Context, json.RawMessage) (any, error) {
			return ipc.DockerListResult{Containers: []ipc.ContainerInfo{
				{Service: "init", State: "exited", ExitCode: 0},
				{Service: "web", State: "running", Health: "healthy"},
			}}, nil
		},
	})
	lostReply := false
	transport := moveTestTransport(func(req *http.Request) (*http.Response, error) {
		reply := httptest.NewRecorder()
		if strings.HasPrefix(req.URL.Path, "/internal/stack-moves/") {
			destination.ServeStackMovePeer(reply, req, "node-a")
			if strings.HasSuffix(req.URL.Path, "/activate") && !lostReply && reply.Code == http.StatusOK {
				lostReply = true
				return nil, errors.New("connection closed after destination started")
			}
		} else {
			incoming := httptest.NewRequest(req.Method, req.URL.RequestURI(), req.Body)
			incoming.Header = req.Header.Clone()
			destination.Handler().ServeHTTP(reply, incoming)
		}
		return reply.Result(), nil
	})
	if err := source.SetWorkloadPeers(t.TempDir(), &http.Client{Transport: transport}, func(node string) (string, bool) {
		return "https://node-b", node == "node-b"
	}); err != nil {
		t.Fatal(err)
	}
	if err := destination.SetWorkloadPeers(t.TempDir(), &http.Client{}, func(node string) (string, bool) {
		return "https://node-a", node == "node-a"
	}); err != nil {
		t.Fatal(err)
	}
	request := `{"destination_node_id":"node-b"}`
	response := peerRequest(t, source, http.MethodPost, path+"/move", request)
	var accepted operation.Operation
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &accepted) != nil || accepted.ID == "" {
		t.Fatalf("move acceptance: %d %s", response.Code, response.Body.String())
	}
	if blocked := peerRequest(t, source, http.MethodPost, path+"/stop", ""); blocked.Code != http.StatusConflict {
		t.Fatalf("move did not fence a concurrent stop: %d %s", blocked.Code, blocked.Body.String())
	}
	runTask := func() error {
		rows, err := sourceDB.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE id=?`, Params: []any{"move-" + accepted.ID}})
		if err != nil || len(rows.Values) != 1 {
			t.Fatalf("missing queued move: %+v %v", rows, err)
		}
		return source.RunStackMove(ctx, json.RawMessage(rows.Values[0][0].(string)))
	}
	if err := runTask(); !errors.Is(err, worker.ErrMoveMustResume) || !lostReply {
		t.Fatalf("lost destination reply should leave the move retryable: %v", err)
	}
	statusPath := "/api/v1/operations/" + accepted.ID
	if result := peerRequest(t, source, http.MethodGet, statusPath, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), "starting-destination") {
		t.Fatalf("move did not retain its retry checkpoint: %d %s", result.Code, result.Body.String())
	}
	if err := runTask(); err != nil {
		t.Fatal(err)
	}
	expected := []string{"seed " + root, "seed " + data, "stop source", "final " + root, "final " + data, "archive source", "activate destination", "start destination"}
	traceMu.Lock()
	observed := slices.Clone(trace)
	traceMu.Unlock()
	if !slices.Equal(observed, expected) {
		t.Fatalf("unexpected handoff sequence: %v", observed)
	}
	if result := peerRequest(t, source, http.MethodGet, statusPath, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"state":"succeeded"`) {
		t.Fatalf("move did not succeed after retry: %d %s", result.Code, result.Body.String())
	}
	if result := peerRequest(t, source, http.MethodGet, path, ""); result.Code != http.StatusOK || !strings.Contains(result.Body.String(), `"primary_node":"node-b"`) {
		t.Fatalf("former owner did not forward to destination: %d %s", result.Code, result.Body.String())
	}
	if replay := peerRequest(t, source, http.MethodPost, path+"/move", request); replay.Code != http.StatusAccepted || !strings.Contains(replay.Body.String(), accepted.ID) {
		t.Fatalf("original move response could not be replayed: %d %s", replay.Code, replay.Body.String())
	}
	targets, err := destination.store.BackupTargets(ctx, id)
	if err != nil || len(targets) != 1 || targets[0].NodeID != "node-a" || targets[0].SourceNodeID != "node-b" || !targets[0].Enabled {
		t.Fatalf("reverse backup target: %+v %v", targets, err)
	}
}

func moveTestHosts(t *testing.T, node string, handlers map[string]ipc.Handler) *hostclient.Pool {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	for command, handler := range handlers {
		daemon.Handle(command, handler)
	}
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	waitForSocket(t, socket)
	hosts := hostclient.NewPool(zap.NewNop())
	hosts.Register(node, socket)
	t.Cleanup(func() {
		hosts.Close()
		cancel()
		<-done
	})
	return hosts
}
