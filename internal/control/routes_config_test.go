package control

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/api/openapi"
	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestManagementCRUDWithSQLite(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewServer(&Config{JWTSecret: "test-secret"}, db, config.NewStore(db), nil, nil, notify.NewBroker(), nil, zap.NewNop())
	token := signedToken(t, "test-secret", []string{"admin"})
	call := func(method, path string, body interface{}, want int) map[string]interface{} {
		t.Helper()
		raw, _ := json.Marshal(body)
		request := httptest.NewRequest(method, "/api/v1"+path, bytes.NewReader(raw))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", time.Now().Format("150405.000000000"))
		response := httptest.NewRecorder()
		s.router.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("%s %s: %d %s (want %d)", method, path, response.Code, response.Body.String(), want)
		}
		if want >= 200 && want < 300 {
			operations, err := openapi.ManagementOperations()
			if err != nil {
				t.Fatal(err)
			}
			for _, op := range operations {
				pattern := strings.Split(op.Path, "/")
				actual := strings.Split("/api/v1"+strings.SplitN(path, "?", 2)[0], "/")
				match := len(pattern) == len(actual)
				if match {
					for i := range pattern {
						if pattern[i] != actual[i] && !strings.HasPrefix(pattern[i], "{") {
							match = false
						}
					}
				}
				if match && op.Method == method {
					if err := openapi.ValidateJSONResponse(method, op.Path, want, response.Body.Bytes()); err != nil {
						t.Fatal(err)
					}
					break
				}
			}
		}
		value := map[string]interface{}{}
		if want != 204 {
			_ = json.Unmarshal(response.Body.Bytes(), &value)
		}
		return value
	}
	call("GET", "/failover/missing/log", nil, 404)
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO failover_steps VALUES('test','old',1,'raft-quorum','complete','2026-01-01T00:00:00Z','2026-01-01T00:00:01Z','old')`},
		{SQL: `INSERT INTO failover_steps VALUES('test','recent',1,'raft-quorum','complete','2026-01-02T00:00:00Z','2026-01-02T00:00:01Z','recent')`},
	}); err != nil {
		t.Fatal(err)
	}
	call("GET", "/failover/test/log", nil, 200)
	network := call("POST", "/networks", map[string]interface{}{"name": "test-net", "cidr": "fd01::/64"}, 201)
	nid := network["id"].(string)
	if network["created_at"] == "" {
		t.Fatal("missing network timestamp")
	}
	call("GET", "/networks/"+nid, nil, 200)
	call("POST", "/networks", map[string]interface{}{"name": "other", "cidr": "fd01::/65"}, 409)
	call("POST", "/networks", map[string]interface{}{"name": "bad", "cidr": "10.0.0.0/24"}, 400)
	call("DELETE", "/networks/"+nid, nil, 204)
	call("GET", "/networks/"+nid, nil, 404)
	rule := call("POST", "/alert-rules", map[string]interface{}{"name": "cpu", "condition": "up == 0", "severity": "critical"}, 201)
	rid := rule["id"].(string)
	changed := call("PUT", "/alert-rules/"+rid, map[string]interface{}{"enabled": false}, 200)
	if changed["enabled"] != false || changed["condition"] != "up == 0" {
		t.Fatalf("partial update: %+v", changed)
	}
	call("GET", "/alert-rules/"+rid, nil, 200)
	call("DELETE", "/alert-rules/"+rid, nil, 204)
	call("GET", "/alert-rules/"+rid, nil, 404)
	notification := call("POST", "/notifications", map[string]interface{}{"title": "test", "body": "message", "severity": "warning", "stack_id": "test-stack"}, 201)
	id := notification["id"].(string)
	call("POST", "/notifications/"+id+"/read", nil, 200)
	call("POST", "/notifications/"+id+"/silence", map[string]string{"until": "invalid"}, 400)
	silenced := call("POST", "/notifications/"+id+"/silence", map[string]string{"until": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, 200)
	if silenced["state"] != "silenced" || silenced["silenced_until"] == nil {
		t.Fatalf("silence response: %+v", silenced)
	}
	resolved := call("POST", "/notifications/"+id+"/resolve", nil, 200)
	if resolved["state"] != "resolved" || resolved["resolved_at"] == nil {
		t.Fatalf("resolve response: %+v", resolved)
	}
	call("DELETE", "/notifications/"+id, nil, 204)
	call("POST", "/notifications/"+id+"/read", nil, 404)
	pool := call("POST", "/network/pools", map[string]string{"name": "tiny", "prefix": "fd02::/127"}, 201)
	pid := pool["id"].(string)
	if err := s.store.CreateStack(ctx, &config.Stack{ID: "test-stack", Name: "test", PrimaryNodeID: "node1"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		call("POST", "/network/allocations", map[string]string{"stack_id": "test-stack", "service": "app", "prefix_pool": pid}, 201)
	}
	call("POST", "/network/allocations", map[string]string{"stack_id": "test-stack", "service": "app", "prefix_pool": pid}, 409)
}

func TestIdempotencyDoesNotOverwriteCompletedAsyncOperation(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := &Server{db: db}
	handler := s.idempotency(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := operationID(r.Context())
		if err := db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE operations SET state='failed',checkpoint='rolled-back' WHERE id=?`, Params: []interface{}{id}}}); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 202, map[string]string{"operation_id": id})
	}))
	request := httptest.NewRequest("POST", "/api/v1/stacks/test/upgrade", strings.NewReader(`{}`))
	request.Header.Set("Idempotency-Key", "async")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 202 {
		t.Fatalf("status %d: %s", response.Code, response.Body.String())
	}
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT state,checkpoint FROM operations`})
	if err != nil || len(result.Values) != 1 || result.Values[0][0] != "failed" || result.Values[0][1] != "rolled-back" {
		t.Fatalf("async completion overwritten: %+v %v", result, err)
	}
}

func TestSnapshotOrderUsesTransactionID(t *testing.T) {
	target := ipc.SnapshotInfoResult{CreatedAt: "2026-10-08T00:00:00Z", CreateTXG: 42}
	newer := target
	newer.CreateTXG = 43
	if !snapshotNewer(newer, target) || snapshotNewer(target, newer) {
		t.Fatal("same-second snapshots must use transaction order")
	}
}
