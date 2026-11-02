// Package monitoring persists producer-owned checks and their incident transitions.
package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
)

type Resource struct {
	HostID        string `json:"host_id,omitempty"`
	WorkloadID    string `json:"workload_id,omitempty"`
	Pool          string `json:"pool,omitempty"`
	Dataset       string `json:"dataset,omitempty"`
	SourceID      string `json:"source_id,omitempty"`
	DestinationID string `json:"destination_id,omitempty"`
}

type Result struct {
	CheckID     string                 `json:"check_id"`
	Producer    string                 `json:"producer"`
	Kind        string                 `json:"kind"`
	Resource    Resource               `json:"resource"`
	Observer    string                 `json:"observer,omitempty"`
	Target      string                 `json:"target,omitempty"`
	Status      string                 `json:"status"` // healthy, failing, unknown, disabled, unsupported
	Reason      string                 `json:"reason,omitempty"`
	ObservedAt  time.Time              `json:"observed_at,omitempty"`
	EvaluatedAt time.Time              `json:"evaluated_at"`
	ExpiresAt   time.Time              `json:"expires_at,omitempty"`
	Labels      map[string]string      `json:"labels,omitempty"`
	Evidence    map[string]interface{} `json:"evidence,omitempty"`
}

type Check struct {
	Result
	LastSuccess time.Time `json:"last_success,omitempty"`
	IncidentID  string    `json:"incident_id,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	Notified    bool      `json:"notified"`
}

// Transition separates the observed check outcome from a policy's incident decision.
type Transition struct {
	Result                           Result
	RuleID, Title, Message, Severity string
	Action                           string // keep, open, resolve, retire (explicit configuration removal)
	Notify                           bool
	// Extra joins producer state to the same guarded transaction.
	Extra       func(incidentID string, guard string, params []interface{}) ([]rqlite.Statement, error)
	Guard       string
	GuardParams []interface{}
}

func Record(ctx context.Context, db *rqlite.Client, broker *notify.Broker, t Transition) error {
	r := t.Result
	if r.CheckID == "" || r.Producer == "" || r.Kind == "" || r.EvaluatedAt.IsZero() {
		return fmt.Errorf("monitoring: check identity, producer, kind and evaluation time required")
	}
	switch r.Status {
	case "healthy", "failing", "unknown", "disabled", "unsupported":
	default:
		return fmt.Errorf("monitoring: invalid check status")
	}
	switch t.Action {
	case "keep", "open", "resolve", "retire":
	default:
		return fmt.Errorf("monitoring: invalid transition")
	}
	if (r.Status == "healthy" || r.Status == "failing") && (r.ObservedAt.IsZero() || r.ObservedAt.After(r.EvaluatedAt) || (!r.ExpiresAt.IsZero() && r.ExpiresAt.Before(r.EvaluatedAt))) {
		return fmt.Errorf("monitoring: positive check result requires fresh evidence")
	}
	if t.Action == "open" && r.Status != "failing" || t.Action == "resolve" && r.Status != "healthy" || t.Action == "retire" && r.Status != "disabled" {
		return fmt.Errorf("monitoring: transition contradicts check status")
	}
	if t.Severity != "info" && t.Severity != "warning" && t.Severity != "critical" {
		return fmt.Errorf("monitoring: invalid severity")
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO monitoring_checks(id,producer,state_json,version) VALUES(?,?,'{}',0)`, Params: []interface{}{r.CheckID, r.Producer}}}); err != nil {
		return err
	}
	for attempt := 0; attempt < 8; attempt++ {
		rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT producer,state_json,version FROM monitoring_checks WHERE id=?`, Params: []interface{}{r.CheckID}})
		if err != nil {
			return err
		}
		if len(rows.Values) != 1 || rows.Values[0][0] != r.Producer {
			return fmt.Errorf("monitoring: check belongs to another producer")
		}
		var old Check
		if err := json.Unmarshal([]byte(rows.Values[0][1].(string)), &old); err != nil {
			return err
		}
		if !r.EvaluatedAt.After(old.EvaluatedAt) {
			return nil
		}
		if t.Action == "open" && (r.ObservedAt.Before(old.ObservedAt) || (old.IncidentID == "" && !r.ObservedAt.After(old.ObservedAt))) {
			return nil
		}
		if t.Action == "resolve" && !r.ObservedAt.After(old.ObservedAt) {
			return nil
		}
		version := rows.Values[0][2]
		guard := ` EXISTS(SELECT 1 FROM monitoring_checks WHERE id=? AND version=?)`
		params := []interface{}{r.CheckID, version}
		if t.Guard != "" {
			guard += " AND (" + t.Guard + ")"
			params = append(params, t.GuardParams...)
		}
		stmt := func(sql string, values ...interface{}) rqlite.Statement {
			return rqlite.Statement{SQL: sql + " AND " + guard, Params: append(values, params...)}
		}
		next := Check{Result: r, LastSuccess: old.LastSuccess, IncidentID: old.IncidentID, Severity: old.Severity, Notified: old.Notified}
		if r.Status == "healthy" && r.ObservedAt.After(next.LastSuccess) {
			next.LastSuccess = r.ObservedAt
		}
		if next.ObservedAt.Before(old.ObservedAt) {
			next.ObservedAt = old.ObservedAt
		}
		now := r.EvaluatedAt.UTC().Format(time.RFC3339Nano)
		labels, err := json.Marshal(r.Labels)
		if err != nil {
			return err
		}
		if r.Labels == nil {
			labels = []byte("{}")
		}
		var statements []rqlite.Statement
		var notification *notify.Notification
		kind := ""
		if t.Action == "open" {
			if next.IncidentID == "" {
				next.IncidentID = uuid.NewString()
				kind = "alert"
				statements = append(statements, stmt(`INSERT INTO alerts(id,rule_id,stack_id,node_id,severity,kind,state,message,created_at,updated_at,labels) SELECT ?,?,?,?,?,?,'active',?,?,?,? WHERE 1=1`, next.IncidentID, t.RuleID, r.Resource.WorkloadID, r.Resource.HostID, t.Severity, r.Kind, t.Message, now, now, string(labels)))
			} else {
				if rank(t.Severity) > rank(old.Severity) {
					kind = "alert.escalated"
				}
				statements = append(statements, stmt(`UPDATE alerts SET severity=?,message=?,labels=?,updated_at=?,stack_id=?,node_id=? WHERE id=?`, t.Severity, t.Message, string(labels), now, r.Resource.WorkloadID, r.Resource.HostID, next.IncidentID))
			}
			next.Severity = t.Severity
		}
		if (t.Action == "resolve" || t.Action == "retire") && next.IncidentID != "" {
			statements = append(statements, stmt(`UPDATE alerts SET state='resolved',resolved_at=?,updated_at=?,resolution_reason=? WHERE id=?`, now, now, nowReason(t), next.IncidentID), stmt(`UPDATE notifications SET resolved_at=? WHERE alert_id=?`, now, next.IncidentID))
			if old.Notified {
				kind = "alert.resolved"
			}
		}
		if t.Notify && kind != "" {
			notification = &notify.Notification{ID: uuid.NewString(), AlertID: next.IncidentID, StackID: r.Resource.WorkloadID, NodeID: r.Resource.HostID, Kind: kind, Severity: t.Severity, Title: t.Title, Body: t.Message, State: "unread", CreatedAt: r.EvaluatedAt}
			statements = append(statements, stmt(`INSERT INTO notifications(id,alert_id,stack_id,node_id,kind,severity,title,body,state,created_at,silenced_until) SELECT ?,?,?,?,?,?,?,?,CASE WHEN julianday(silenced_until)>julianday(?) THEN 'silenced' ELSE 'unread' END,?,silenced_until FROM alerts WHERE id=?`, notification.ID, notification.AlertID, notification.StackID, notification.NodeID, kind, t.Severity, t.Title, t.Message, now, now, next.IncidentID))
			next.Notified = true
		}
		if t.Action == "resolve" || t.Action == "retire" {
			next.IncidentID = ""
			next.Notified = false
		}
		data, err := json.Marshal(next)
		if err != nil {
			return err
		}
		observationID := uuid.NewString()
		resultJSON, err := json.Marshal(r)
		if err != nil {
			return err
		}
		statements = append(statements, stmt(`INSERT INTO monitoring_observations(id,check_id,recorded_at,result_json) SELECT ?,?,?,? WHERE 1=1`, observationID, r.CheckID, now, string(resultJSON)), stmt(`UPDATE monitoring_checks SET state_json=?,version=version+1 WHERE id=?`, string(data), r.CheckID))
		if t.Extra != nil {
			extra, err := t.Extra(next.IncidentID, `EXISTS(SELECT 1 FROM monitoring_observations WHERE id=?)`, []interface{}{observationID})
			if err != nil {
				return err
			}
			statements = append(statements, extra...)
		}
		if err := db.Execute(ctx, statements); err != nil {
			return err
		}
		committed, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM monitoring_observations WHERE id=?`, Params: []interface{}{observationID}})
		if err != nil {
			return err
		}
		if len(committed.Values) == 0 {
			if t.Guard != "" {
				return nil
			}
			continue
		}
		if notification != nil && broker != nil {
			visible, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM notifications WHERE id=? AND state!='silenced'`, Params: []interface{}{notification.ID}})
			if err != nil {
				return err
			}
			if len(visible.Values) > 0 {
				body, _ := json.Marshal(notification)
				broker.PublishJSON("notification.new", body)
			}
		}
		return nil
	}
	return fmt.Errorf("monitoring: concurrent check updates; retry observation")
}

func nowReason(t Transition) string {
	if t.Result.Reason != "" {
		return t.Result.Reason
	}
	return "recovered"
}
func rank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}
