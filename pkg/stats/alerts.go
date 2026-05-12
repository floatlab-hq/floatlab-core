package stats

import (
	"encoding/json"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// alertmanagerPayload is the subset of the Alertmanager webhook payload that
// vmalert sends.
type alertmanagerPayload struct {
	Alerts []struct {
		Status      string            `json:"status"` // "firing" | "resolved"
		Labels      map[string]string `json:"labels"`
		Annotations map[string]string `json:"annotations"`
		StartsAt    time.Time         `json:"startsAt"`
		EndsAt      time.Time         `json:"endsAt"`
	} `json:"alerts"`
}

// WebhookHandler returns an http.HandlerFunc that accepts vmalert Alertmanager
// webhook POST requests and creates notifications in rqlite + broker.
func WebhookHandler(db *rqlite.Client, broker *notify.Broker, log *zap.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var payload alertmanagerPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		for _, a := range payload.Alerts {
			if a.Status != "firing" {
				continue
			}
			severity := a.Labels["severity"]
			if severity == "" {
				severity = "warning"
			}
			title := a.Annotations["summary"]
			if title == "" {
				title = a.Labels["alertname"]
			}
			body := a.Annotations["description"]
			stackID := a.Labels["stack_id"]
			_ = stackID

			n := &notify.Notification{
				Kind:     "alert",
				Severity: severity,
				Title:    title,
				Body:     body,
			}
			if err := notify.Create(r.Context(), db, broker, n); err != nil {
				log.Error("stats webhook: create notification", zap.Error(err))
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}
