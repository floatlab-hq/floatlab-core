package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func TestCreateAndStartPersistsNominatedSecondary(t *testing.T) {
	server, _ := peerWorkloadServer(t, "primary")
	for _, node := range []string{"primary", "secondary"} {
		if err := server.store.CreateNode(t.Context(), &config.Node{ID: node, Name: node}); err != nil {
			t.Fatal(err)
		}
	}
	response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/web/start", `x-fl-stack:
  secondary_node: secondary
services:
  web: {image: alpine}
`)
	var result map[string]string
	if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &result) != nil {
		t.Fatalf("create-and-start failed: %d %s", response.Code, response.Body.String())
	}
	stack, err := server.store.GetStack(t.Context(), result["stack_id"])
	if err != nil || stack.PrimaryNodeID != "primary" || stack.BackupNodeID != "secondary" {
		t.Fatalf("lost the nominated secondary: %+v %v", stack, err)
	}
}

func TestComposeUpdateCanNominateAnUnassignedSecondary(t *testing.T) {
	server, _ := peerWorkloadServer(t, "node-a")
	for _, node := range []string{"node-a", "node-b", "node-c"} {
		if err := server.store.CreateNode(t.Context(), &config.Node{ID: node, Name: node}); err != nil {
			t.Fatal(err)
		}
	}
	stack := &config.Stack{ID: "workload", Name: "web", PrimaryNodeID: "node-a", ComposeYAML: strings.Replace(validStackCompose, "  secondary_node: node-b\n", "", 1)}
	if err := server.store.CreateStack(t.Context(), stack); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/stacks/workload/compose"
	validate := `{"stack_id":"workload","compose_file":` + jsonString(validStackCompose) + `}`
	if response := peerRequest(t, server, http.MethodPost, "/api/v1/stacks/validate", validate); response.Code != http.StatusNoContent {
		t.Fatalf("valid nomination rejected: %d %s", response.Code, response.Body.String())
	}
	body := `{"compose_file":` + jsonString(validStackCompose) + `}`
	if response := peerRequest(t, server, http.MethodPut, path, body); response.Code != http.StatusOK {
		t.Fatalf("nomination failed: %d %s", response.Code, response.Body.String())
	}
	saved, err := server.store.GetStack(t.Context(), stack.ID)
	if err != nil || saved.BackupNodeID != "node-b" {
		t.Fatalf("nomination not recorded: %+v %v", saved, err)
	}
	if response := peerRequest(t, server, http.MethodPut, path+"?change", strings.Replace(body, "secondary_node: node-b", "secondary_node: node-c", 1)); response.Code != http.StatusBadRequest {
		t.Fatalf("existing nomination was replaceable: %d %s", response.Code, response.Body.String())
	}
}

func TestReplicaFailoverRequiresConfirmationAndACompletePoint(t *testing.T) {
	for _, mode := range []string{"clone", "takeover"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			const id = "5afb34d9-7fa4-46a7-a5eb-f4c073cda364"
			const root = "floatlab/data"
			const project = "name: data\nservices:\n  app:\n    image: alpine\n    volumes: [first:/first, second:/second]\nvolumes:\n  first: {}\n  second: {}\n"
			destination, db := peerWorkloadServer(t, "node-b")
			source, shared := peerWorkloadServer(t, "node-a")
			destination.cfg.LabDB, source.cfg.LabDB = shared, shared
			nodes := []string{"node-a", "node-b"}
			if mode == "takeover" {
				nodes = append(nodes, "node-c")
			}
			for _, node := range nodes {
				if err := destination.store.CreateNode(ctx, &config.Node{ID: node, Name: node}); err != nil {
					t.Fatal(err)
				}
			}
			replicas := t.TempDir()
			replica, err := rqlite.OpenLocal(filepath.Join(replicas, "node-a.sqlite"), "node-a")
			if err != nil {
				t.Fatal(err)
			}
			stack := &config.Stack{ID: id, Name: "data", PrimaryNodeID: "node-a", BackupNodeID: "node-b", ZFSDataset: root, ComposeYAML: project}
			for _, update := range []func() error{
				func() error { return rqlite.Migrate(ctx, replica) },
				func() error { return config.Migrate(ctx, replica) },
				func() error { return config.NewStore(replica).CreateStack(ctx, stack) },
				func() error { return source.store.CreateStack(ctx, stack) },
				func() error {
					return replica.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO local_migrations(name,applied_at) VALUES('managed-workload-startup-v1',?)`, Params: []any{time.Now()}}})
				},
			} {
				if err := update(); err != nil {
					t.Fatal(err)
				}
			}
			if err := replica.Close(); err != nil {
				t.Fatal(err)
			}
			offline := moveTestTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("peer fenced") })
			lookup := func(node string) (string, bool) { return "https://" + node, true }
			if err := destination.SetWorkloadPeers(replicas, &http.Client{Transport: offline}, lookup); err != nil {
				t.Fatal(err)
			}
			if err := source.SetWorkloadPeers(t.TempDir(), &http.Client{Transport: offline}, lookup); err != nil {
				t.Fatal(err)
			}
			destination.hosts = moveTestHosts(t, "node-b", map[string]ipc.Handler{
				"fs.dataset.list": func(context.Context, json.RawMessage) (any, error) { return ipc.DatasetListResult{}, nil },
				"fs.backup.recovery-points": func(_ context.Context, raw json.RawMessage) (any, error) {
					var request ipc.BackupRecoveryPointsPayload
					if err := json.Unmarshal(raw, &request); err != nil {
						return nil, err
					}
					points := []store.BackupSnapshot{{Name: "fl-000001", GUID: "123"}}
					if request.Dataset == root+"/first" {
						points = append(points, store.BackupSnapshot{Name: "fl-000002", GUID: "124"})
					}
					return points, nil
				},
			})
			path := "/api/v1/failover/" + id
			response := peerRequest(t, destination, http.MethodGet, path+"/preview?destination_node_id=node-b", "")
			var preview replicaFailoverPreview
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &preview) != nil || preview.Mode != mode || preview.Snapshot != "fl-000001" || len(preview.Datasets) != 2 || preview.PotentialDataLoss <= 0 {
				t.Fatalf("invalid %s preview: %d %s", mode, response.Code, response.Body.String())
			}
			response = peerRequest(t, destination, http.MethodPost, path+"/trigger?no-fence", `{"destination_node_id":"node-b"}`)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("accepted an unfenced source: %d %s", response.Code, response.Body.String())
			}
			response = peerRequest(t, destination, http.MethodPost, path+"/trigger?old-snapshot", `{"destination_node_id":"node-b","primary_fenced":true,"recovery_point":"changed"}`)
			if response.Code != http.StatusConflict {
				t.Fatalf("accepted a stale point: %d %s", response.Code, response.Body.String())
			}
			request := replicaFailoverRequest{DestinationNodeID: "node-b", PrimaryFenced: true, RecoveryPoint: preview.RecoveryPoint}
			if mode == "clone" {
				request.Name = "data-recovery"
			}
			body, _ := json.Marshal(request)
			response = peerRequest(t, destination, http.MethodPost, path+"/trigger", string(body))
			var result map[string]string
			if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &result) != nil || result["operation_id"] == "" || (result["stack_id"] == id) != (mode == "takeover") {
				t.Fatalf("queued %s: %d %s", mode, response.Code, response.Body.String())
			}
			rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE id=?`, Params: []any{"backup-restore-" + result["operation_id"]}})
			var restore worker.BackupRestorePayload
			if err != nil || len(rows.Values) != 1 || json.Unmarshal([]byte(fmt.Sprint(rows.Values[0][0])), &restore) != nil || len(restore.ExpectedGUIDs) != 2 {
				t.Fatalf("missing verified restore: %+v %v", rows, err)
			}
			owner, err := sharedStackOwner(ctx, shared, id)
			if err != nil || owner.node != "" || (restore.OwnerGeneration != 0) != (mode == "takeover") {
				t.Fatalf("early ownership claim: %+v %v", owner, err)
			}
			if mode == "takeover" {
				if pending, err := destination.store.GetStack(ctx, id); err == nil || pending != nil {
					t.Fatalf("uncommitted takeover was published as a workload: %+v %v", pending, err)
				}
				if err := destination.CommitReplicaTakeover(ctx, restore); err != nil {
					t.Fatal(err)
				}
				if err := destination.CommitReplicaTakeover(ctx, restore); err != nil {
					t.Fatalf("could not replay committed takeover: %v", err)
				}
				restore.OperationID = "competing-operation"
				if err := destination.CommitReplicaTakeover(ctx, restore); err == nil {
					t.Fatal("competing operation overwrote takeover owner")
				}
				if owner, err := source.workloadOwner(ctx, id, ""); err != nil || owner != "node-b" {
					t.Fatalf("stale source resolved itself as owner: %s %v", owner, err)
				}
			}
		})
	}
}
