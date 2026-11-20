package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
			if conflict.Code != http.StatusConflict {
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
