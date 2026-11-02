package stats

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func TestWebhookIndependentResourcesRecoveryAndRecurrence(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	handler := WebhookHandler(db, nil, zap.NewNop())
	base := time.Now().Add(-time.Minute).UTC()
	post := func(status, destination string, at time.Time) {
		t.Helper()
		alert := map[string]interface{}{"status": status, "labels": map[string]string{"alertname": "transfer", "node_id": "source", "dest_node_id": destination}, "startsAt": base, "endsAt": at}
		if status == "firing" {
			alert["startsAt"] = at
		}
		body, _ := json.Marshal(map[string]interface{}{"alerts": []interface{}{alert}})
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/webhook", strings.NewReader(string(body))))
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
	}
	post("firing", "a", base)
	post("firing", "a", base)
	post("firing", "b", base)
	post("resolved", "a", base.Add(time.Second))
	post("firing", "a", base.Add(2*time.Second))
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT (SELECT count(*) FROM alerts),(SELECT count(*) FROM alerts WHERE state='active'),(SELECT count(*) FROM notifications)`})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Values[0]; got[0] != float64(3) || got[1] != float64(2) || got[2] != float64(4) {
		t.Fatalf("lost per-destination recurrence/recovery: %v", got)
	}
	// An old resolution cannot clear the new occurrence.
	post("resolved", "a", base.Add(time.Second))
	post("firing", "a", base)
	rows, err = db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM alerts WHERE state='active'`})
	if err != nil || len(rows.Values) != 2 {
		t.Fatalf("stale recovery resolved recurrence: %v %v", rows, err)
	}
}
