package control

import (
	"net/http"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestUpgradeEnqueueFailureDoesNotCommitUpdating(t *testing.T) {
	s, db := peerWorkloadServer(t, "node-a")
	const id = "8b8f390d-a92a-4901-80dc-579287cfd477"
	if err := s.store.CreateStack(t.Context(), &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/web", ComposeYAML: "services:\n  app: {image: 'alpine:3.20'}\n"}); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().UTC()
	if err := s.raft.Apply(run.StackStateChanged{StackID: id, To: run.StateRunningPrimary, Timestamp: observed, NodeID: "node-a"}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := db.Execute(t.Context(), []rqlite.Statement{{SQL: `CREATE TRIGGER reject_upgrade_task BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT, 'enqueue unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, s, http.MethodPost, "/api/v1/stacks/"+id+"/upgrade", `{"images":{"app":"alpine:3.21"}}`)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response %d: %s", response.Code, response.Body.String())
	}
	state, _ := s.raft.FSM().State(id)
	if state.State != run.StateRunningPrimary || !state.UpdatedAt.Equal(observed) {
		t.Fatalf("failed enqueue stranded lifecycle: %+v", state)
	}
}

func TestBackupRestoreEnqueueFailureLeavesNoStartableStack(t *testing.T) {
	s, db, _, _ := restoreControlFixture(t)
	if err := db.Execute(t.Context(), []rqlite.Statement{{SQL: `CREATE TRIGGER reject_restore_task BEFORE INSERT ON tasks BEGIN SELECT RAISE(ABORT,'enqueue unavailable'); END`}}); err != nil {
		t.Fatal(err)
	}
	body := backupRestoreBody([]worker.BackupRestoreSnapshot{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}})
	response := callBackupRestore(t, s, "restore-atomic", body)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("response %d: %s", response.Code, response.Body.String())
	}
	rows, err := db.Query(t.Context(), rqlite.Statement{SQL: `SELECT count(*) FROM stacks`})
	if err != nil || rows.Values[0][0] != float64(0) {
		t.Fatalf("failed admission left startable config: %+v %v", rows, err)
	}
	if len(s.raft.FSM().AllStates()) != 0 {
		t.Fatal("failed admission changed lifecycle")
	}
}
