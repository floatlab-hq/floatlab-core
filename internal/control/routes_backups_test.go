package control

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/api/openapi"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestBackupTargetAPIValidationAndCleanupContract(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewStore(db)
	for _, id := range []string{"source", "backup"} {
		if err := cfg.CreateNode(ctx, &config.Node{ID: id, Name: id, Hostname: id, Addresses: []config.NodeAddress{{Type: "LAN-6", Address: "fd00::2"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.CreateStack(ctx, &config.Stack{ID: "app", Name: "demo", PrimaryNodeID: "source", ZFSDataset: "floatlab/demo", ComposeYAML: "name: demo\nservices:\n  app:\n    image: nginx\n    volumes:\n      - type: bind\n        source: ./data\n        target: /data\n        x-fl-snapshots: h/24\n"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(&Config{JWTSecret: "test-secret"}, db, cfg, nil, nil, notify.NewBroker(), nil, zap.NewNop())
	token := signedToken(t, "test-secret", []string{"admin"})
	path := "/api/v1/storage/replication/app/targets"
	call := func(method, body string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", time.Now().Format("150405.000000000"))
		resp := httptest.NewRecorder()
		s.router.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("%s %s = %d %s", method, body, resp.Code, resp.Body.String())
		}
		if want >= 200 && want < 300 {
			if err := openapi.ValidateJSONResponse(method, "/api/v1/storage/replication/{stack_id}/targets", want, resp.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
		}
		return resp
	}
	for _, body := range []string{
		`{"dataset":"floatlab/demo/data","node_id":"source","schedule":"h/24"}`,
		`{"dataset":"floatlab/other/data","node_id":"backup","schedule":"h/24"}`,
		`{"dataset":"floatlab/demo/data","node_id":"unknown","schedule":"h/24"}`,
		`{"dataset":"floatlab/demo/data","node_id":"backup","schedule":"h/0"}`,
	} {
		call("PUT", body, 400)
	}
	body := `{"dataset":"floatlab/demo/data","node_id":"backup","schedule":"h/24 d/28"}`
	resp := call("PUT", body, 200)
	var saved config.BackupTarget
	if err := json.Unmarshal(resp.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Enabled || saved.SourceNodeID != "source" || saved.Generation < 1 || saved.LocalPolicy != "1h/24" {
		t.Fatalf("invalid saved target %+v", saved)
	}
	if !bytes.Contains(call("GET", "", 200).Body.Bytes(), []byte(`"node_id":"backup"`)) {
		t.Fatal("missing target")
	}
	trigger := httptest.NewRequest("POST", "/api/v1/storage/replication/app/trigger", nil)
	trigger.Header.Set("Authorization", "Bearer "+token)
	trigger.Header.Set("Idempotency-Key", "trigger-backup")
	triggered := httptest.NewRecorder()
	s.router.ServeHTTP(triggered, trigger)
	if triggered.Code != 202 {
		t.Fatalf("trigger: %d %s", triggered.Code, triggered.Body.String())
	}
	queued, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE type='snapshot.create'`})
	if err != nil || len(queued.Values) != 1 {
		t.Fatalf("queued snapshot: %+v %v", queued, err)
	}
	var scheduled struct {
		Policy              string           `json:"policy"`
		TransferGenerations map[string]int64 `json:"transfer_generations"`
		TransferTargets     []string         `json:"transfer_targets"`
	}
	if err := json.Unmarshal([]byte(queued.Values[0][0].(string)), &scheduled); err != nil {
		t.Fatal(err)
	}
	if scheduled.Policy != "1h/24" || len(scheduled.TransferGenerations) != 0 || len(scheduled.TransferTargets) != 0 {
		t.Fatalf("manual backup lost source retention or pins unsent history: %+v", scheduled)
	}
	call("DELETE", `{"dataset":"floatlab/demo/data","node_id":"backup"}`, 202)
	var targets []config.BackupTarget
	if err := json.Unmarshal(call("GET", "", 200).Body.Bytes(), &targets); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Enabled || !targets[0].CleanupPending || targets[0].Generation <= saved.Generation {
		t.Fatalf("removal lost fencing %+v", targets)
	}
	call("PUT", body, 409)
	s.raft = testRaftNode(t)
	if err := s.raft.Apply(run.StackStateChanged{StackID: "app", NodeID: "backup", To: run.StateRunningBackup, Timestamp: time.Now().UTC()}, time.Second); err != nil {
		t.Fatal(err)
	}
	failoverTrigger := httptest.NewRequest("POST", "/api/v1/storage/replication/app/trigger", nil)
	failoverTrigger.Header.Set("Authorization", "Bearer "+token)
	failoverTrigger.Header.Set("Idempotency-Key", "failover-backup")
	failoverResponse := httptest.NewRecorder()
	s.router.ServeHTTP(failoverResponse, failoverTrigger)
	if failoverResponse.Code != 409 || !strings.Contains(failoverResponse.Body.String(), "failover node") {
		t.Fatalf("stale source replication allowed: %d %s", failoverResponse.Code, failoverResponse.Body.String())
	}
}
