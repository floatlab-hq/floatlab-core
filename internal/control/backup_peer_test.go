package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestBackupRestoreRoutesToDestinationBeforeIdempotency(t *testing.T) {
	owner, ownerDB, _, _ := restoreControlFixture(t)
	endpoint := httptest.NewTLSServer(owner.Handler())
	defer endpoint.Close()
	front, frontDB := peerWorkloadServer(t, "front")
	if err := front.SetWorkloadPeers(t.TempDir(), endpoint.Client(), func(node string) (string, bool) {
		return endpoint.URL, node == "backup"
	}); err != nil {
		t.Fatal(err)
	}
	body := backupRestoreBody([]worker.BackupRestoreSnapshot{{Dataset: "floatlab/original/data", Snapshot: "fl-000000"}, {Dataset: "floatlab/original/cache", Snapshot: "fl-000000"}})
	response := callBackupRestore(t, front, "remote-restore", body)
	if response.Code != http.StatusAccepted {
		t.Fatalf("forwarded restore: %d %s", response.Code, response.Body.String())
	}
	var result map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"idempotency_keys", "stacks", "operations"} {
		rows, err := frontDB.Query(context.Background(), rqlite.Statement{SQL: "SELECT count(*) FROM " + table})
		if err != nil || rows.Values[0][0] != float64(0) {
			t.Fatalf("front consumed destination %s: %+v %v", table, rows, err)
		}
	}
	rows, err := ownerDB.Query(context.Background(), rqlite.Statement{SQL: `SELECT count(*) FROM idempotency_keys`})
	if err != nil || rows.Values[0][0] != float64(1) {
		t.Fatalf("destination did not own idempotency: %+v %v", rows, err)
	}
	rows, err = frontDB.Query(context.Background(), rqlite.Statement{SQL: `SELECT node_id FROM workload_operation_owners WHERE operation_id=?`, Params: []interface{}{result["operation_id"]}})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != "backup" {
		t.Fatalf("forwarded operation lost destination owner: %+v %v", rows, err)
	}
	operation := peerRequest(t, front, http.MethodGet, "/api/v1/operations/"+result["operation_id"], "")
	if operation.Code != http.StatusOK || !strings.Contains(operation.Body.String(), result["stack_id"]) {
		t.Fatalf("operation lookup did not follow destination: %d %s", operation.Code, operation.Body.String())
	}
	retry := callBackupRestore(t, front, "remote-restore", body)
	if retry.Code != http.StatusAccepted || retry.Body.String() != response.Body.String() {
		t.Fatalf("forwarded idempotent retry changed result: %d %s", retry.Code, retry.Body.String())
	}
}

func TestBackupInternalEndpointsAreNotOnPublicAdminRouter(t *testing.T) {
	s, _, _, commands := restoreControlFixture(t)
	for _, path := range []string{"/internal/backups/snapshots?source_node_id=offline-source&dataset=floatlab/original/data", "/internal/backups/prepare"} {
		response := peerRequest(t, s, http.MethodGet, path, "")
		if response.Code != http.StatusNotFound {
			t.Fatalf("internal endpoint exposed to public admin: %d %s", response.Code, response.Body.String())
		}
	}
	if len(*commands) != 0 {
		t.Fatalf("public route reached hostd: %v", *commands)
	}
	request := httptest.NewRequest(http.MethodPost, "/internal/backups/prepare", strings.NewReader(`{"backup_node_id":"another-node"}`))
	response := httptest.NewRecorder()
	s.ServeBackupPeer(response, request)
	if response.Code != http.StatusBadRequest || len(*commands) != 0 {
		t.Fatalf("peer accepted another backup node: %d %s", response.Code, response.Body.String())
	}
}

func TestBackupPeerPreparationRejectsInsecureDestination(t *testing.T) {
	s, _ := peerWorkloadServer(t, "destination")
	if err := s.SetWorkloadPeers(t.TempDir(), http.DefaultClient, func(string) (string, bool) { return "http://insecure.example", true }); err != nil {
		t.Fatal(err)
	}
	_, err := s.PrepareBackupRestore(context.Background(), ipc.BackupRestorePreparePayload{BackupNodeID: "backup"})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("insecure peer allowed: %v", err)
	}
}
