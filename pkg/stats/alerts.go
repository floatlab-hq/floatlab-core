package stats

import (
	"encoding/json"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"net/http"
	"time"
)

type alertmanagerPayload struct {
	Alerts []struct {
		Status       string            `json:"status"`
		Labels       map[string]string `json:"labels"`
		Annotations  map[string]string `json:"annotations"`
		StartsAt     time.Time         `json:"startsAt"`
		EndsAt       time.Time         `json:"endsAt"`
		GeneratorURL string            `json:"generatorURL"`
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
			identityLabels := map[string]string{}
			for key, value := range a.Labels {
				if key != "severity" {
					identityLabels[key] = value
				}
			}
			identity, _ := json.Marshal(identityLabels)
			key := uuid.NewSHA1(uuid.NameSpaceOID, append([]byte(ruleID+"\x00"), identity...)).String()
			now := time.Now().UTC()
			status, action := "failing", "open"
			observed := a.StartsAt
			if a.Status == "resolved" {
				status, action = "healthy", "resolve"
				observed = a.EndsAt
			}
			if observed.IsZero() {
				observed = now
			}
			if err := monitoring.Record(r.Context(), db, broker, monitoring.Transition{
				Result: monitoring.Result{CheckID: "webhook:" + key, Producer: "webhook", Kind: "metrics", Resource: monitoring.Resource{HostID: nodeID, WorkloadID: stackID, Pool: a.Labels["pool"], Dataset: a.Labels["dataset"], SourceID: a.Labels["source_node_id"], DestinationID: a.Labels["dest_node_id"]}, Observer: r.RemoteAddr, Status: status, ObservedAt: observed, EvaluatedAt: now, Labels: a.Labels, Evidence: map[string]interface{}{"generator_url": a.GeneratorURL}},
				RuleID: ruleID, Title: title, Message: body, Severity: severity, Action: action, Notify: true,
			}); err != nil {
				log.Error("persist alert observation", zap.Error(err))
				http.Error(w, "alert persistence failed", 500)
				return
			}
		}
		w.WriteHeader(200)
	}
}
