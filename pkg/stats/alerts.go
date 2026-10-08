package stats

import (
	"encoding/json"
	"fmt"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"net/http"
	"time"
)

type alertmanagerPayload struct {
	Alerts []struct {
		Status      string            `json:"status"`
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
		EndsAt      time.Time         `json:"endsAt"`
	} `json:"alerts"`
}

// WebhookHandler persists alert state and fans out notifications on first firing.
func WebhookHandler(db *rqlite.Client, broker *notify.Broker, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload alertmanagerPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Alerts == nil {
			http.Error(w, "alerts array is required", 400)
			return
		}
		for _, a := range payload.Alerts {
			if a.Status != "firing" && a.Status != "resolved" {
				http.Error(w, "invalid alert status", 400)
				return
			}
			severity := a.Labels["severity"]
			if severity == "" {
				severity = "warning"
			}
			if severity != "info" && severity != "warning" && severity != "critical" {
				http.Error(w, "invalid alert severity", 400)
				return
			}
			ruleID := a.Labels["rule_id"]
			if ruleID == "" {
				ruleID = a.Labels["alertname"]
			}
			if ruleID == "" {
				http.Error(w, "rule_id or alertname is required", 400)
				return
			}
			title := a.Annotations["summary"]
			if title == "" {
				title = a.Labels["alertname"]
			}
			body := a.Annotations["description"]
			stackID, nodeID := a.Labels["stack_id"], a.Labels["node_id"]
			id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(ruleID+"\x00"+stackID+"\x00"+nodeID)).String()
			previous, err := db.Query(r.Context(), rqlite.Statement{SQL: `SELECT state FROM alerts WHERE id=?`, Params: []interface{}{id}})
			if err != nil {
				log.Error("alert lookup", zap.Error(err))
				http.Error(w, "alert lookup failed", 500)
				return
			}
			state := "active"
			var resolved interface{}
			if a.Status == "resolved" {
				state = "resolved"
				ended := a.EndsAt
				if ended.IsZero() {
					ended = time.Now().UTC()
				}
				resolved = ended.UTC().Format(time.RFC3339Nano)
			}
			created := a.StartsAt
			if created.IsZero() {
				created = time.Now().UTC()
			}
			if err := db.Execute(r.Context(), []rqlite.Statement{{SQL: `INSERT INTO alerts(id,rule_id,stack_id,node_id,severity,kind,state,message,created_at,resolved_at) VALUES(?,?,?,?,?,'metrics',?,?,?,?) ON CONFLICT(id) DO UPDATE SET severity=excluded.severity,state=excluded.state,message=excluded.message,resolved_at=excluded.resolved_at`, Params: []interface{}{id, ruleID, stackID, nodeID, severity, state, body, created.UTC().Format(time.RFC3339Nano), resolved}}}); err != nil {
				log.Error("persist alert", zap.Error(err))
				http.Error(w, "alert persistence failed", 500)
				return
			}
			firstFiring := a.Status == "firing" && (len(previous.Values) == 0 || previous.Values[0][0] != "active")
			if firstFiring {
				if err := notify.Create(r.Context(), db, broker, &notify.Notification{AlertID: id, StackID: stackID, NodeID: nodeID, Kind: "alert", Severity: severity, Title: title, Body: body}); err != nil {
					log.Error("alert notification", zap.Error(err))
					http.Error(w, fmt.Sprintf("notification persistence failed: %v", err), 500)
					return
				}
			}
		}
		w.WriteHeader(200)
	}
}
