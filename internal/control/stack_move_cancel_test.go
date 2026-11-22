package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestRejectedMoveReservationCancelsDurablyBeforeReleasingSource(t *testing.T) {
	ctx := t.Context()
	source, sourceDB := peerWorkloadServer(t, "node-a")
	destination, destinationDB := peerWorkloadServer(t, "node-b")
	source.cfg.LabDB, destination.cfg.LabDB = sourceDB, sourceDB
	move := stackMove{
		OperationID: "ebdcb4a8-ec2e-4d22-a46f-f19a7f2583b0", StackID: "5afb34d9-7fa4-46a7-a5eb-f4c073cda364",
		Destination: "node-b", NetworkPool: "deleted-pool", Generation: 1,
	}
	move.Stack = config.Stack{ID: move.StackID, Name: "web", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/web", ComposeYAML: "services:\n  web:\n    image: alpine\n"}
	if err := source.store.CreateStack(ctx, &move.Stack); err != nil {
		t.Fatal(err)
	}
	if err := source.raft.Apply(run.StackStateChanged{StackID: move.StackID, To: run.StateRunningPrimary, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := source.ops.Create(ctx, operation.Operation{ID: move.OperationID, StackID: move.StackID, Action: "move", Actor: "test", State: "running", Checkpoint: "reserving-destination", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := worker.EnqueueTask(ctx, sourceDB, "move-"+move.OperationID, worker.TaskStackMove, move.StackID, move); err != nil {
		t.Fatal(err)
	}
	// A prior reserve lost its response; the pool was subsequently removed.
	if err := destinationDB.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO workload_move_reservations(operation_id,stack_id,name) VALUES(?,?,?)`, Params: []any{move.OperationID, move.StackID, move.Stack.Name}},
		{SQL: `INSERT INTO network_allocations(id,pool_id,stack_id,address,state,created_at,updated_at) VALUES('address','deleted-pool',?,'192.0.2.200','pending',?,?)`, Params: []any{move.StackID, time.Now().UTC(), time.Now().UTC()}},
	}); err != nil {
		t.Fatal(err)
	}
	destination.hosts = moveTestHosts(t, "node-b", map[string]ipc.Handler{
		"fs.dataset.list": func(context.Context, json.RawMessage) (any, error) { return ipc.DatasetListResult{}, nil },
	})
	lostCancelReply := false
	transport := moveTestTransport(func(request *http.Request) (*http.Response, error) {
		reply := httptest.NewRecorder()
		destination.ServeStackMovePeer(reply, request, "node-a")
		if strings.HasSuffix(request.URL.Path, "/cancel") && reply.Code == http.StatusOK && !lostCancelReply {
			lostCancelReply = true
			return nil, errors.New("lost cancellation acknowledgment")
		}
		return reply.Result(), nil
	})
	if err := source.SetWorkloadPeers(t.TempDir(), &http.Client{Transport: transport}, func(node string) (string, bool) { return "https://node-b", node == "node-b" }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(move)
	if err := source.RunStackMove(ctx, raw); !errors.Is(err, worker.ErrMoveMustResume) {
		t.Fatalf("lost cancellation reply must remain retryable: %v", err)
	}
	op, err := source.ops.Get(ctx, move.OperationID)
	if err != nil || op.Checkpoint != "canceling-reservation" || op.State != "running" || !lostCancelReply {
		t.Fatalf("source released before cancellation acknowledgment: %+v %v", op, err)
	}
	for _, table := range []string{"network_allocations", "workload_move_reservations"} {
		rows, err := destinationDB.Query(ctx, rqlite.Statement{SQL: `SELECT 1 FROM ` + table + ` WHERE stack_id=?`, Params: []any{move.StackID}})
		if err != nil || len(rows.Values) != 0 {
			t.Fatalf("cancellation retained %s: %+v %v", table, rows, err)
		}
	}
	if err := source.RunStackMove(ctx, raw); err != nil {
		t.Fatal(err)
	}
	op, err = source.ops.Get(ctx, move.OperationID)
	if err != nil || op.State != "failed" || op.Checkpoint != "reservation-canceled" || !strings.Contains(op.Error, "409 Conflict") {
		t.Fatalf("canceled operation did not terminate: %+v %v", op, err)
	}
	instance, _ := source.raft.FSM().State(move.StackID)
	if instance.State != run.StateRunningPrimary {
		t.Fatalf("pre-cutover rejection stopped source: %+v", instance)
	}
	// Late reserve/activate requests cannot recreate released ownership resources.
	hash, err := move.payloadHash()
	if err != nil {
		t.Fatal(err)
	}
	if err := sourceDB.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO stack_move_grants(operation_id,stack_id,payload_hash) VALUES(?,?,?)`, Params: []any{move.OperationID, move.StackID, hash}}}); err != nil {
		t.Fatal(err)
	}
	if err := destination.cancelStackMoveReservation(ctx, move); err != nil {
		t.Fatalf("completed cancellation was not idempotent: %v", err)
	}
	for _, action := range []string{"reserve", "activate"} {
		request := httptest.NewRequest(http.MethodPost, "/internal/stack-moves/"+action, strings.NewReader(string(raw)))
		reply := httptest.NewRecorder()
		destination.ServeStackMovePeer(reply, request, "node-a")
		if reply.Code != http.StatusConflict || !strings.Contains(reply.Body.String(), "canceled") {
			t.Fatalf("late %s was not fenced: %d %s", action, reply.Code, reply.Body.String())
		}
	}
	if response := peerRequest(t, source, http.MethodPost, "/api/v1/stacks/"+move.StackID+"/stop", ""); response.Code != http.StatusAccepted {
		t.Fatalf("canceled move retained source operation lane: %d %s", response.Code, response.Body.String())
	}
}

func TestMoveCancellationPreservesCommittedAndUnrelatedResources(t *testing.T) {
	for _, state := range []string{"handoff", "adopted", "unrelated"} {
		t.Run(state, func(t *testing.T) {
			ctx := t.Context()
			destination, db := peerWorkloadServer(t, "node-b")
			destination.cfg.LabDB = db
			move := stackMove{OperationID: "ebdcb4a8-ec2e-4d22-a46f-f19a7f2583b0", StackID: "5afb34d9-7fa4-46a7-a5eb-f4c073cda364", Destination: "node-b"}
			if err := db.Execute(ctx, []rqlite.Statement{
				{SQL: `INSERT INTO workload_move_reservations(operation_id,stack_id,name) VALUES(?,?,'web')`, Params: []any{move.OperationID, move.StackID}},
				{SQL: `INSERT INTO network_allocations(id,pool_id,stack_id,address,state,created_at,updated_at) VALUES('address','pool',?,'192.0.2.200','pending',?,?)`, Params: []any{move.StackID, time.Now().UTC(), time.Now().UTC()}},
			}); err != nil {
				t.Fatal(err)
			}
			switch state {
			case "handoff":
				if err := destination.grantStackMove(ctx, move.OperationID, move); err != nil {
					t.Fatal(err)
				}
			case "adopted":
				if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO workload_move_receipts(operation_id,stack_id,phase) VALUES(?,?,'pending')`, Params: []any{move.OperationID, move.StackID}}}); err != nil {
					t.Fatal(err)
				}
			case "unrelated":
				if err := destination.store.CreateStack(ctx, &config.Stack{ID: move.StackID, Name: "web", PrimaryNodeID: "node-b"}); err != nil {
					t.Fatal(err)
				}
			}
			err := destination.cancelStackMoveReservation(ctx, move)
			if (state == "unrelated") != (err == nil) {
				t.Fatalf("unsafe cancellation result: %v", err)
			}
			rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT 1 FROM network_allocations WHERE stack_id=?`, Params: []any{move.StackID}})
			if err != nil || len(rows.Values) != 1 {
				t.Fatalf("cancellation released owned resources: %+v %v", rows, err)
			}
		})
	}
}

func TestMoveActivationRejectionRemainsFailClosed(t *testing.T) {
	ctx := t.Context()
	source, db := peerWorkloadServer(t, "node-a")
	source.cfg.LabDB = db
	move := stackMove{OperationID: "ebdcb4a8-ec2e-4d22-a46f-f19a7f2583b0", StackID: "5afb34d9-7fa4-46a7-a5eb-f4c073cda364", Destination: "node-b", Stack: config.Stack{PrimaryNodeID: "node-a"}}
	if err := source.ops.Create(ctx, operation.Operation{ID: move.OperationID, StackID: move.StackID, Action: "move", Actor: "test", State: "running", Checkpoint: "starting-destination", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	transport := moveTestTransport(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/activate") {
			t.Errorf("post-cutover failure attempted %s", request.URL.Path)
		}
		reply := httptest.NewRecorder()
		writeError(reply, http.StatusConflict, "activation rejected")
		return reply.Result(), nil
	})
	if err := source.SetWorkloadPeers(t.TempDir(), &http.Client{Transport: transport}, func(string) (string, bool) { return "https://node-b", true }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(move)
	if err := source.RunStackMove(ctx, raw); !errors.Is(err, worker.ErrMoveMustResume) {
		t.Fatalf("post-cutover rejection released uncertain ownership: %v", err)
	}
	op, err := source.ops.Get(ctx, move.OperationID)
	if err != nil || op.State != "running" || op.Checkpoint != "starting-destination" {
		t.Fatalf("post-cutover operation became terminal: %+v %v", op, err)
	}
}
