package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

const backupRestoreCompose = `name: original
services:
  app:
    image: alpine
    volumes:
      - type: bind
        source: ./data
        target: /data
      - type: bind
        source: ./cache
        target: /cache
`

func restoreControlFixture(t *testing.T) (*Server, *rqlite.Client, *string, *[]string) {
	t.Helper()
	s, db := peerWorkloadServer(t, "backup")
	if err := s.store.CreateNode(context.Background(), &config.Node{ID: "backup", Name: "backup"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	socketDir, err := os.MkdirTemp("/tmp", "restore-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "restore.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	collision := ""
	commands := []string{}
	daemon.Handle("fs.snapshot.list", func(_ context.Context, raw json.RawMessage) (any, error) {
		commands = append(commands, "fs.snapshot.list")
		var p ipc.SnapshotListPayload
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(p.Dataset, "floatlab/backups/offline-source/floatlab/original/") {
			return nil, fmt.Errorf("unexpected backup path %s", p.Dataset)
		}
		return ipc.SnapshotListResult{Snapshots: []ipc.SnapshotInfoResult{{Name: "fl-000000", Dataset: p.Dataset}}}, nil
	})
	daemon.Handle("fs.dataset.list", func(context.Context, json.RawMessage) (any, error) {
		commands = append(commands, "fs.dataset.list")
		result := ipc.DatasetListResult{Datasets: []ipc.DatasetInfoResult{}}
		if collision != "" {
			result.Datasets = append(result.Datasets, ipc.DatasetInfoResult{Name: collision})
		}
		return result, nil
	})
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	waitForSocket(t, socket)
	pool := hostclient.NewPool(zap.NewNop())
	pool.Register("backup", socket)
	s.hosts = pool
	t.Cleanup(func() { cancel(); <-done; pool.Close() })
	return s, db, &collision, &commands
}

func backupRestoreBody(snapshots []worker.BackupRestoreSnapshot) backupRestoreRequest {
	return backupRestoreRequest{Name: "restored", SourceNodeID: "offline-source", BackupNodeID: "backup", DestinationNodeID: "backup", ComposeFile: backupRestoreCompose, Snapshots: snapshots}
}

func callBackupRestore(t *testing.T, s *Server, key string, body backupRestoreRequest) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/storage/backups/restore", strings.NewReader(string(raw)))
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", key)
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	return response
}

func TestBackupRestoreValidatesCompleteMountMapBeforeCreatingStack(t *testing.T) {
	s, db, _, commands := restoreControlFixture(t)
	for i, snapshots := range [][]worker.BackupRestoreSnapshot{
		nil,
		{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}},
		{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/data", Snapshot: "fl-000000"}},
		{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/other/cache", Snapshot: "fl-000000"}},
		{{Dataset: "floatlab/original/data", Snapshot: "user-manual"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}},
		{{Dataset: "floatlab/original/data", Snapshot: "fl-000001"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}},
	} {
		response := callBackupRestore(t, s, fmt.Sprint(i), backupRestoreBody(snapshots))
		if response.Code != 400 {
			t.Fatalf("invalid map accepted: %d %s", response.Code, response.Body.String())
		}
	}
	for _, table := range []string{"stacks", "tasks"} {
		rows, err := db.Query(context.Background(), rqlite.Statement{SQL: "SELECT count(*) FROM " + table})
		if err != nil || rows.Values[0][0] != float64(0) {
			t.Fatalf("invalid request mutated %s: %+v %v", table, rows, err)
		}
	}
	for _, command := range *commands {
		if command != "fs.snapshot.list" {
			t.Fatalf("invalid request reached root lookup/mutation: %v", *commands)
		}
	}
}

func TestBackupRestoreQueuesNewStackWhileOriginalSourceIsOffline(t *testing.T) {
	s, db, _, _ := restoreControlFixture(t)
	body := backupRestoreBody([]worker.BackupRestoreSnapshot{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}})
	response := callBackupRestore(t, s, "restore", body)
	if response.Code != 202 {
		t.Fatalf("restore: %d %s", response.Code, response.Body.String())
	}
	var result struct{ OperationID, StackID string }
	var values map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &values); err != nil {
		t.Fatal(err)
	}
	result.OperationID, result.StackID = values["operation_id"], values["stack_id"]
	stack, err := s.store.GetStack(context.Background(), result.StackID)
	if err != nil || stack.Name != "restored" || stack.PrimaryNodeID != "backup" || stack.ZFSDataset != "floatlab/restored" || !strings.Contains(stack.ComposeYAML, "name: restored") {
		t.Fatalf("wrong restored stack %+v %v", stack, err)
	}
	state, ok := s.raft.FSM().State(stack.ID)
	if !ok || state.State != run.StateRestoring {
		t.Fatalf("workload started before data restore: %+v", state)
	}
	op, err := s.ops.Get(context.Background(), result.OperationID)
	if err != nil || op.Action != "start" || op.StackID != stack.ID {
		t.Fatalf("operation cannot complete through normal start: %+v %v", op, err)
	}
	rows, err := db.Query(context.Background(), rqlite.Statement{SQL: `SELECT type,payload FROM tasks WHERE id=?`, Params: []interface{}{"backup-restore-" + result.OperationID}})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != worker.TaskBackupRestore {
		t.Fatalf("missing restore task: %+v %v", rows, err)
	}
	var payload worker.BackupRestorePayload
	if err := json.Unmarshal([]byte(rows.Values[0][1].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SourceNodeID != "offline-source" || payload.DestinationNodeID != "backup" || len(payload.Snapshots) != 2 {
		t.Fatalf("lost backup identity: %+v", payload)
	}
	for _, taskState := range []string{"pending", "failed"} {
		if err := db.Execute(context.Background(), []rqlite.Statement{
			{SQL: `UPDATE tasks SET state=? WHERE id=?`, Params: []interface{}{taskState, "backup-restore-" + result.OperationID}},
			{SQL: `UPDATE operations SET state='failed' WHERE id=?`, Params: []interface{}{result.OperationID}},
		}); err != nil {
			t.Fatal(err)
		}
		if taskState == "failed" {
			if err := s.raft.Apply(run.StackStateChanged{StackID: stack.ID, NodeID: "backup", From: run.StateRestoring, To: run.StateFailed, Timestamp: time.Now().UTC()}, time.Second); err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/stacks/"+stack.ID+"/start", nil)
		request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
		request.Header.Set("Idempotency-Key", "manual-start-"+taskState)
		blocked := httptest.NewRecorder()
		s.Handler().ServeHTTP(blocked, request)
		if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "backup restore data is incomplete") {
			t.Fatalf("%s restore allowed manual start before all mounts activate: %d %s", taskState, blocked.Code, blocked.Body.String())
		}
	}
}

func TestBackupRestoreRejectsDestinationDatasetCollision(t *testing.T) {
	s, db, collision, _ := restoreControlFixture(t)
	*collision = "floatlab/restored/orphaned-volume"
	response := callBackupRestore(t, s, "collision", backupRestoreBody([]worker.BackupRestoreSnapshot{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}}))
	if response.Code != 409 {
		t.Fatalf("dataset collision: %d %s", response.Code, response.Body.String())
	}
	rows, err := db.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM stacks`})
	if err != nil || rows.Values[0][0] != float64(0) {
		t.Fatalf("collision created a stack: %+v %v", rows, err)
	}
}
