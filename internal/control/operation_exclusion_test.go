package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestOperationExclusion(t *testing.T) {
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	const stackPath = "/api/v1/stacks/" + id
	const datasetPath = "/api/v1/storage/datasets/" + id + "/snapshots/point"
	const project = "name: web\nservices:\n  web:\n    image: alpine\n"
	for _, test := range []struct {
		name, path string
		state      run.State
	}{
		{"restart", stackPath + "/restart", run.StateRunningPrimary},
		{"restore", stackPath + "/snapshots/point/restore", run.StateIdle},
		{"rollback", datasetPath + "/restore", run.StateIdle},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			server, db := peerWorkloadServer(t, "node-a")
			if err := server.store.CreateStack(ctx, &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/web", ComposeYAML: project}); err != nil {
				t.Fatal(err)
			}
			if err := server.raft.Apply(run.StackStateChanged{StackID: id, To: test.state, Timestamp: time.Now().UTC()}, time.Second); err != nil {
				t.Fatal(err)
			}
			if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO recovery_points(id,stack_id,snapshot_id,dataset_path,compose_yaml,created_at) VALUES('point',?,?,?,?,?)`, Params: []any{id, "point", "floatlab/web", project, time.Now().UTC()}}}); err != nil {
				t.Fatal(err)
			}
			server.hosts = moveTestHosts(t, "node-a", map[string]ipc.Handler{
				"fs.snapshot.list": func(context.Context, json.RawMessage) (any, error) {
					return ipc.SnapshotListResult{Snapshots: []ipc.SnapshotInfoResult{{Dataset: "floatlab/web", Name: "point"}}}, nil
				},
			})
			accepted := peerRequest(t, server, http.MethodPost, test.path, "")
			if accepted.Code != http.StatusAccepted {
				t.Fatalf("admission: %d %s", accepted.Code, accepted.Body.String())
			}
			replay := peerRequest(t, server, http.MethodPost, test.path, "")
			if replay.Code != accepted.Code || replay.Body.String() != accepted.Body.String() {
				t.Fatalf("idempotent replay changed admission: %d %s", replay.Code, replay.Body.String())
			}
			duplicate := httptest.NewRequest(http.MethodPost, test.path, nil)
			duplicate.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
			duplicate.Header.Set("Idempotency-Key", "different-key")
			conflict := httptest.NewRecorder()
			server.Handler().ServeHTTP(conflict, duplicate)
			if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "stack already has an active operation") {
				t.Fatalf("different key admitted duplicate operation: %d %s", conflict.Code, conflict.Body.String())
			}
			for _, next := range []struct{ method, path string }{
				{http.MethodPost, stackPath + "/start"},
				{http.MethodPost, stackPath + "/stop"},
				{http.MethodDelete, stackPath},
				{http.MethodDelete, datasetPath},
				{http.MethodPost, datasetPath + "/restore"},
			} {
				if next.method == http.MethodPost && next.path == test.path {
					continue
				}
				blocked := peerRequest(t, server, next.method, next.path, "")
				if blocked.Code != http.StatusConflict {
					t.Errorf("%s %s bypassed queued operation: %d %s", next.method, next.path, blocked.Code, blocked.Body.String())
				}
			}
			tasks, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT id FROM tasks WHERE stack_id=?`, Params: []any{id}})
			if err != nil || len(tasks.Values) != 1 {
				t.Fatalf("replay/conflicting requests created extra work: %+v %v", tasks, err)
			}
			var operation struct {
				ID string `json:"operation_id"`
			}
			if err := json.Unmarshal(accepted.Body.Bytes(), &operation); err != nil {
				t.Fatal(err)
			}
			if err := server.ops.Update(ctx, operation.ID, "succeeded", "succeeded", ""); err != nil {
				t.Fatal(err)
			}
			next := stackPath + "/start"
			if test.state == run.StateRunningPrimary {
				next = stackPath + "/stop"
			}
			if response := peerRequest(t, server, http.MethodPost, next, ""); response.Code != http.StatusAccepted {
				t.Fatalf("terminal operation did not release stack: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestOperationReservationStorageFailureIsNotConflict(t *testing.T) {
	ctx := t.Context()
	s, db := peerWorkloadServer(t, "node-a")
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	if err := s.store.CreateStack(ctx, &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/web", ComposeYAML: "services: {}"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `CREATE TRIGGER deny_operation BEFORE INSERT ON operations BEGIN SELECT RAISE(ABORT,'operation store unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	reply := peerRequest(t, s, http.MethodPost, "/api/v1/stacks/"+id+"/snapshots", "")
	if reply.Code != http.StatusServiceUnavailable || !strings.Contains(reply.Body.String(), "could not reserve operation") {
		t.Fatalf("reservation storage failure: %d %s", reply.Code, reply.Body.String())
	}
}

func TestOperationStackIdentity(t *testing.T) {
	for path, want := range map[string]string{
		"/api/v1/stacks/web/restart":                           "web",
		"/api/v1/storage/datasets/web/snapshots/point/restore": "web",
		"/api/v1/failover/web/trigger":                         "web",
		"/api/v1/failover/web/abort":                           "",
		"/api/v1/nodes/node-a/updates/host/prepare":            "",
		"/api/v1/storage/replication/web/targets":              "",
	} {
		if got := stackIDFromPath(path); got != want {
			t.Errorf("%s: got stack %q, want %q", path, got, want)
		}
	}
}

func TestProjectSnapshotRestoreSelectsPointSource(t *testing.T) {
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	const saved = "name: web\nservices:\n  web:\n    image: alpine:3.21\n"
	const captured = "name: web\nservices:\n  web:\n    image: alpine:3.20\n"
	for _, tc := range []struct {
		name, stored, want string
		reads              int32
	}{
		{"existing recovery point", saved, saved, 0},
		{"new project recovery point", "", captured, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, db := peerWorkloadServer(t, "node-a")
			s.store = config.NewProjectStore(db, nil, func(context.Context, *config.Stack) (string, error) {
				return "", os.ErrNotExist
			}, func(context.Context, *config.Stack) error { return nil })
			stack := &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/web", ComposeYAML: saved}
			if err := s.store.CreateStack(ctx, stack); err != nil {
				t.Fatal(err)
			}
			if err := s.store.PublishStackCompose(ctx, stack); err != nil {
				t.Fatal(err)
			}
			if err := s.raft.Apply(run.StackStateChanged{StackID: id, To: run.StateIdle, Timestamp: time.Now().UTC()}, time.Second); err != nil {
				t.Fatal(err)
			}
			if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO recovery_points(id,stack_id,snapshot_id,dataset_path,compose_yaml,created_at) VALUES('point',?,?,?,?,?)`, Params: []any{id, "point", "floatlab/web", tc.stored, time.Now().UTC()}}}); err != nil {
				t.Fatal(err)
			}
			var reads atomic.Int32
			s.hosts = moveTestHosts(t, "node-a", map[string]ipc.Handler{
				"compose.source.read": func(context.Context, json.RawMessage) (any, error) {
					reads.Add(1)
					return ipc.ComposeSourceReadResult{ComposeFile: captured}, nil
				},
			})
			reply := peerRequest(t, s, http.MethodPost, "/api/v1/stacks/"+id+"/snapshots/point/restore", "")
			if reply.Code != http.StatusAccepted {
				t.Fatalf("restore admission: %d %s", reply.Code, reply.Body.String())
			}
			if got := reads.Load(); got != tc.reads {
				t.Fatalf("read ZFS Compose %d times, wanted %d", got, tc.reads)
			}
			rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE stack_id=?`, Params: []any{id}})
			if err != nil || len(rows.Values) != 1 {
				t.Fatalf("restore task: %+v %v", rows, err)
			}
			var task worker.StackRestorePayload
			if err := json.Unmarshal([]byte(rows.Values[0][0].(string)), &task); err != nil {
				t.Fatal(err)
			}
			if task.SourceCompose != tc.want {
				t.Fatalf("restore source is %q, want %q", task.SourceCompose, tc.want)
			}
		})
	}
}
