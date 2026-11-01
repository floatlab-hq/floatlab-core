package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/notify"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func peerWorkloadServer(t *testing.T, node string) (*Server, *rqlite.Client) {
	t.Helper()
	ctx := context.Background()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "local.sqlite"), node)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := floatraft.NewLocalNode(ctx, db, node, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(&Config{LocalNodeID: node, JWTSecret: "test-secret"}, db, config.NewStore(db), lifecycle, nil, notify.NewBroker(), nil, zap.NewNop())
	return server, db
}

func TestRemoteNodeHealthUsesItsOwnerAndRejectsAnotherHop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, db := peerWorkloadServer(t, "node-b")
	if err := config.NewStore(db).CreateNode(ctx, &config.Node{ID: "node-b", Name: "node-b"}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "hostd.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	daemon.Handle("sys.info", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"hostname": "node-b"}, nil
	})
	daemon.Handle("sys.version", func(context.Context, json.RawMessage) (any, error) {
		return map[string]string{"host_version": "0.1.0-dev"}, nil
	})
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	waitForSocket(t, socket)
	pool := hostclient.NewPool(zap.NewNop())
	defer pool.Close()
	pool.Register("node-b", socket)
	owner.hosts = pool
	if err := owner.SetWorkloadPeers(t.TempDir(), &http.Client{}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewTLSServer(owner.Handler())
	defer endpoint.Close()
	front, _ := peerWorkloadServer(t, "node-a")
	if err := front.SetWorkloadPeers(t.TempDir(), endpoint.Client(), func(node string) (string, bool) { return endpoint.URL, node == "node-b" }); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, front, http.MethodGet, "/api/v1/nodes/node-b/health", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"status":"reachable"`) {
		t.Fatalf("remote health %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/health", nil)
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
	request.Header.Set("X-FloatLab-Workload-Hop", "node-c")
	response = httptest.NewRecorder()
	owner.Handler().ServeHTTP(response, request)
	if response.Code != 503 {
		t.Fatalf("misrouted peer health followed another hop: %d %s", response.Code, response.Body.String())
	}
	cancel()
	<-done
}

func peerRequest(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
	request.Header.Set("Idempotency-Key", method+path)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	return response
}

func TestRemoteBackupManagementRoutesToWorkloadOwner(t *testing.T) {
	ctx := context.Background()
	owner, ownerDB := peerWorkloadServer(t, "node-b")
	for _, id := range []string{"node-b", "node-c"} {
		if err := owner.store.CreateNode(ctx, &config.Node{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	st := &config.Stack{ID: "remote-app", Name: "remote", PrimaryNodeID: "node-b", ZFSDataset: "floatlab/remote", ComposeYAML: "name: remote\nservices:\n  app:\n    image: nginx\n    volumes:\n      - type: bind\n        source: ./data\n        target: /data\n        x-fl-snapshots: h/24\n"}
	if err := owner.store.CreateStack(ctx, st); err != nil {
		t.Fatal(err)
	}
	endpoint := httptest.NewTLSServer(owner.Handler())
	defer endpoint.Close()
	front, frontDB := peerWorkloadServer(t, "node-a")
	replicas := t.TempDir()
	createReplicaStack(t, replicas, "node-b", st.ID, st.Name)
	if err := front.SetWorkloadPeers(replicas, endpoint.Client(), func(node string) (string, bool) { return endpoint.URL, node == "node-b" }); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/storage/replication/remote-app/targets"
	response := peerRequest(t, front, http.MethodPut, path, `{"dataset":"floatlab/remote/data","node_id":"node-c","schedule":"h/24 d/28"}`)
	if response.Code != 200 {
		t.Fatalf("forwarded target update: %d %s", response.Code, response.Body.String())
	}
	response = peerRequest(t, front, http.MethodGet, path, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"node_id":"node-c"`) {
		t.Fatalf("forwarded target list: %d %s", response.Code, response.Body.String())
	}
	if err := ownerDB.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO tasks(id,type,stack_id,payload,state,attempts,created_at,updated_at) VALUES('foreign-job','repl.trigger','remote-app','{"dataset":"floatlab/remote/data","dest_node_id":"node-c"}','done',0,datetime('now'),datetime('now'))`}}); err != nil {
		t.Fatal(err)
	}
	response = peerRequest(t, front, http.MethodGet, "/api/v1/storage/replication?stack_id=remote-app", "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "foreign-job") {
		t.Fatalf("forwarded history: %d %s", response.Code, response.Body.String())
	}
	local, err := frontDB.Query(ctx, rqlite.Statement{SQL: `SELECT count(*) FROM idempotency_keys`})
	if err != nil || local.Values[0][0] != float64(0) {
		t.Fatalf("front consumed owner idempotency: %+v %v", local, err)
	}
	local, err = frontDB.Query(ctx, rqlite.Statement{SQL: `SELECT count(*) FROM backup_targets`})
	if err != nil || local.Values[0][0] != float64(0) {
		t.Fatalf("front wrote foreign target: %+v %v", local, err)
	}
}

func TestWorkloadOwnerIgnoresUnpublishedReplicaStagingFiles(t *testing.T) {
	server, _ := peerWorkloadServer(t, "node-a")
	directory := t.TempDir()
	createReplicaStack(t, directory, "node-b", "remote-app", "remote")
	createReplicaStack(t, directory, "node-c", "staged-app", "staged")
	if err := os.Rename(filepath.Join(directory, "node-c.sqlite"), filepath.Join(directory, ".node-c.staging.sqlite")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".node-d.staging.sqlite"), []byte("incomplete receive"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := server.SetWorkloadPeers(directory, &http.Client{}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	owner, err := server.workloadOwner(context.Background(), "remote-app", "")
	if err != nil || owner != "node-b" {
		t.Fatalf("published owner unavailable while replica receive is in progress: owner=%q err=%v", owner, err)
	}
	if err := os.Rename(filepath.Join(directory, "node-b.sqlite"), filepath.Join(directory, "wrong.sqlite")); err != nil {
		t.Fatal(err)
	}
	if _, err := server.workloadOwner(context.Background(), "remote-app", ""); err == nil || !strings.Contains(err.Error(), "filename/source mismatch") {
		t.Fatalf("published replica identity mismatch was accepted: %v", err)
	}
}

func TestPeerCreationAndOperationPollRouteBeforeLocalIdempotency(t *testing.T) {
	owner, ownerDB := peerWorkloadServer(t, "node-b")
	endpoint := httptest.NewTLSServer(owner.Handler())
	defer endpoint.Close()
	front, frontDB := peerWorkloadServer(t, "node-a")
	replicas := t.TempDir()
	if err := front.SetWorkloadPeers(replicas, endpoint.Client(), func(node string) (string, bool) { return endpoint.URL, node == "node-b" }); err != nil {
		t.Fatal(err)
	}
	compose := strings.Replace(validStackCompose, "primary_node: node-a", "primary_node: node-b", 1)
	response := peerRequest(t, front, http.MethodPost, "/api/v1/stacks", `{"name":"remote-web","compose_file":`+jsonString(compose)+`}`)
	if response.Code != 201 {
		t.Fatalf("forwarded create: %d %s", response.Code, response.Body.String())
	}
	local, _ := frontDB.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM idempotency_keys`})
	if local.Values[0][0] != float64(0) {
		t.Fatal("front consumed owner idempotency", local)
	}
	local, _ = frontDB.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM stacks`})
	if local.Values[0][0] != float64(0) {
		t.Fatal("front created foreign workload", local)
	}
	remote, _ := ownerDB.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM stacks`})
	if remote.Values[0][0] != float64(1) {
		t.Fatal("owner missing stack", remote)
	}
	var created struct {
		ID          string `json:"id"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatal("missing created stack", response.Body.String(), err)
	}
	if err := owner.raft.Apply(run.StackStateChanged{StackID: created.ID, NodeID: "node-b", To: run.StateIdle, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	createReplicaStack(t, replicas, "node-b", created.ID, "remote-web")
	validation := peerRequest(t, front, http.MethodPost, "/api/v1/stacks/validate", `{"stack_id":`+jsonString(created.ID)+`,"compose_file":`+jsonString(compose)+`}`)
	if validation.Code != 204 {
		t.Fatalf("remote edit validation: %d %s", validation.Code, validation.Body.String())
	}
	response = peerRequest(t, front, http.MethodPost, "/api/v1/stacks/"+created.ID+"/start", "")
	if response.Code != 202 {
		t.Fatalf("forwarded start: %d %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.OperationID == "" {
		t.Fatal("missing async operation", response.Body.String(), err)
	}
	poll := peerRequest(t, front, http.MethodGet, "/api/v1/operations/"+created.OperationID, "")
	if poll.Code != 200 || !strings.Contains(poll.Body.String(), created.OperationID) {
		t.Fatalf("operation poll before replica arrives: %d %s", poll.Code, poll.Body.String())
	}
}

func TestCreateAndStartUsesLocalOwnerInsteadOfFirstCachedNode(t *testing.T) {
	server, db := peerWorkloadServer(t, "node-b")
	for _, id := range []string{"node-a", "node-b"} {
		if err := config.NewStore(db).CreateNode(context.Background(), &config.Node{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/new-web/start", "services:\n  web:\n    image: nginx:alpine\n")
	if response.Code != 202 {
		t.Fatalf("create-start %d %s", response.Code, response.Body.String())
	}
	stacks, err := server.store.ListStacks(context.Background())
	if err != nil || len(stacks) != 1 || stacks[0].PrimaryNodeID != "node-b" {
		t.Fatalf("wrong local owner: %+v %v", stacks, err)
	}
}

func createReplicaStack(t *testing.T, dir, node, id, name string) {
	t.Helper()
	ctx := context.Background()
	db, err := rqlite.OpenLocal(filepath.Join(dir, node+".sqlite"), node)
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
	if err = config.NewStore(db).CreateStack(ctx, &config.Stack{ID: id, Name: name, PrimaryNodeID: node}); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := floatraft.NewLocalNode(ctx, db, node, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err = lifecycle.Apply(run.StackStateChanged{StackID: id, NodeID: node, To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestPeerStackViewsAreStaleReadOnlyAndUnreachableOwnerFails(t *testing.T) {
	server, db := peerWorkloadServer(t, "node-a")
	dir := t.TempDir()
	createReplicaStack(t, dir, "node-b", "remote-id", "same-name")
	if err := config.NewStore(db).CreateStack(context.Background(), &config.Stack{ID: "local-id", Name: "same-name", PrimaryNodeID: "node-a"}); err != nil {
		t.Fatal(err)
	}
	if err := server.SetWorkloadPeers(dir, &http.Client{}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, server, http.MethodGet, "/api/v1/stacks", "")
	if response.Code != 200 {
		t.Fatalf("list %d %s", response.Code, response.Body.String())
	}
	var stacks []stackResponse
	if err := json.Unmarshal(response.Body.Bytes(), &stacks); err != nil {
		t.Fatal(err)
	}
	if len(stacks) != 2 {
		t.Fatal(stacks)
	}
	remote := stacks[1]
	if !remote.Replica || !remote.Stale || remote.OwnerNode != "node-b" || remote.ObservedRevision == 0 || remote.ObservedAt == nil || remote.State != string(run.StateRunningPrimary) {
		t.Fatalf("missing stale provenance: %+v", remote)
	}
	response = peerRequest(t, server, http.MethodPost, "/api/v1/stacks/remote-id/stop", "")
	if response.Code != 503 || !strings.Contains(response.Body.String(), "workload_owner_unavailable") {
		t.Fatalf("unreachable owner %d %s", response.Code, response.Body.String())
	}
	rows, _ := db.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM operations`})
	if rows.Values[0][0] != float64(0) {
		t.Fatal("remote view ran an operation locally", rows)
	}
	nodeStacks := peerRequest(t, server, http.MethodGet, "/api/v1/nodes/node-b/stacks", "")
	if nodeStacks.Code != 200 {
		t.Fatalf("remote node stacks: %d %s", nodeStacks.Code, nodeStacks.Body.String())
	}
	if err := json.Unmarshal(nodeStacks.Body.Bytes(), &stacks); err != nil || len(stacks) != 1 || stacks[0].ID != "remote-id" || !stacks[0].Stale {
		t.Fatalf("missing remote node view: %s %v", nodeStacks.Body.String(), err)
	}
}

func TestEnabledLabBlocksLegacyUnfencedTakeover(t *testing.T) {
	server, db := peerWorkloadServer(t, "node-a")
	if err := server.SetWorkloadPeers(t.TempDir(), &http.Client{}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	if err := config.NewStore(db).CreateStack(context.Background(), &config.Stack{ID: "owned", Name: "web", PrimaryNodeID: "node-a", BackupNodeID: "node-b"}); err != nil {
		t.Fatal(err)
	}
	if err := server.raft.Apply(run.StackStateChanged{StackID: "owned", NodeID: "node-a", To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	before, _ := server.raft.FSM().State("owned")
	for _, path := range []string{"/api/v1/stacks/owned/failover", "/api/v1/stacks/owned/restore", "/api/v1/failover/owned/trigger"} {
		response := peerRequest(t, server, http.MethodPost, path, "")
		if response.Code != 409 || !strings.Contains(response.Body.String(), "coordinated_takeover_required") {
			t.Fatalf("unfenced takeover: %s %d %s", path, response.Code, response.Body.String())
		}
		after, _ := server.raft.FSM().State("owned")
		if *before != *after {
			t.Fatalf("rejected takeover changed lifecycle: %+v %+v", before, after)
		}
	}
}
