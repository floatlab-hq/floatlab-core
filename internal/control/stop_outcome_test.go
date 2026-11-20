package control

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestStopIdleCompletesOperation(t *testing.T) {
	server, _ := peerWorkloadServer(t, "node-a")
	const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
	if err := server.store.CreateStack(t.Context(), &config.Stack{ID: id, Name: "web", PrimaryNodeID: "node-a"}); err != nil {
		t.Fatal(err)
	}
	if err := server.raft.Apply(run.StackStateChanged{StackID: id, To: run.StateIdle, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/"+id+"/stop", "")
	if response.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", response.Code, response.Body.String())
	}
	var body struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	op, err := server.ops.Get(t.Context(), body.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "succeeded" {
		t.Fatalf("idle stop did not finish: %+v", op)
	}
	state, _ := server.raft.FSM().State(id)
	if state.State != run.StateIdle {
		t.Fatalf("idle stop changed state: %+v", state)
	}
}
