package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/api/openapi"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func TestBuiltInAlertRuleOverrides(t *testing.T) {
	ctx := context.Background()
	db, err := rqlite.OpenLocal(filepath.Join(t.TempDir(), "alerts.sqlite"), "local")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	newServer := func() *Server {
		return NewServer(&Config{JWTSecret: "test-secret"}, db, config.NewStore(db), nil, nil, notify.NewBroker(), nil, zap.NewNop())
	}
	s := newServer()
	token := signedToken(t, "test-secret", []string{"admin"})
	const ruleID = "service.memory.high"
	const path = "/api/v1/alert-rules/" + ruleID
	call := func(method, url, body string, status int) map[string]interface{} {
		t.Helper()
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", time.Now().Format(time.RFC3339Nano))
		w := httptest.NewRecorder()
		s.router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d, want %d: %s", method, url, w.Code, status, w.Body.String())
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		if w.Code == 200 {
			pattern := strings.Replace(url, ruleID, "{id}", 1)
			if err := openapi.ValidateJSONResponse(method, pattern, status, w.Body.Bytes()); err != nil {
				t.Fatal(err)
			}
		}
		return decoded
	}
	expect := func(row map[string]interface{}, overridden, enabled bool, warning, critical float64) {
		t.Helper()
		thresholds, _ := row["thresholds"].(map[string]interface{})
		if row["built_in"] != true || row["overridden"] != overridden || row["enabled"] != enabled || thresholds["warning"] != warning || thresholds["critical"] != critical {
			t.Fatalf("unexpected rule: %#v", row)
		}
	}
	expect(call(http.MethodGet, path, "", 200), false, true, 90, 97)
	expect(call(http.MethodPut, path, `{"thresholds":{"warning":91},"enabled":false}`, 200), true, false, 91, 97)
	expect(call(http.MethodPut, path, `{"thresholds":{"critical":98}}`, 200), true, false, 91, 98)
	call(http.MethodPut, path, `{"thresholds":{"warning":99}}`, 400)
	call(http.MethodPut, path, `{"thresholds":{"warning":null}}`, 400)
	call(http.MethodPut, path, `{"condition":"other"}`, 400)
	call(http.MethodDelete, path, "", 409)
	s = newServer()
	expect(call(http.MethodGet, path, "", 200), true, false, 91, 98)
	resolved, err := s.builtInAlertRule(ctx, ruleID)
	if err != nil || resolved.Enabled || resolved.Thresholds["warning"] != 91 {
		t.Fatalf("evaluator has different effective rule: %#v, %v", resolved, err)
	}
	expect(call(http.MethodPost, path+"/reset", "", 200), false, true, 90, 97)
	expect(call(http.MethodGet, path, "", 200), false, true, 90, 97)
}
