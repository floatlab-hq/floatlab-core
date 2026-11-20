package control

import (
	"net/http"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestRetryFailedBackupStopUsesCommittedHost(t *testing.T) {
	server, _ := peerWorkloadServer(t, "backup")
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	if err := server.store.CreateStack(t.Context(), &config.Stack{ID: id, Name: "web", PrimaryNodeID: "primary", BackupNodeID: "backup"}); err != nil {
		t.Fatal(err)
	}
	if err := server.raft.Apply(run.StackStateChanged{StackID: id, To: run.StateFailed, NodeID: "backup", Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/"+id+"/stop", "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("backup retry routed to primary: %d %s", response.Code, response.Body.String())
	}
}
