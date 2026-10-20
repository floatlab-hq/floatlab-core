package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
)

type storedState struct {
	ID      string
	Version int64
	State
}

func Migrate(ctx context.Context, db *rqlite.Client) error {
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `CREATE TABLE IF NOT EXISTS alert_evaluations (id TEXT PRIMARY KEY, rule_id TEXT NOT NULL, state_json TEXT NOT NULL DEFAULT '{}', version INTEGER NOT NULL DEFAULT 0)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_alert_evaluations_rule ON alert_evaluations(rule_id)`},
	}); err != nil {
		return err
	}
	return rqlite.AddColumns(ctx, db, "alerts", []string{"labels TEXT NOT NULL DEFAULT '{}'", "updated_at TEXT", "resolution_reason TEXT"})
}

func loadStates(ctx context.Context, db *rqlite.Client) (map[string][]storedState, error) {
	result, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id,rule_id,state_json,version FROM alert_evaluations`})
	if err != nil {
		return nil, err
	}
	out := map[string][]storedState{}
	for _, row := range result.Values {
		state := storedState{ID: row[0].(string), Version: int64(row[3].(float64))}
		if err := json.Unmarshal([]byte(row[2].(string)), &state.State); err != nil {
			return nil, fmt.Errorf("alert state %s: %w", state.ID, err)
		}
		out[row[1].(string)] = append(out[row[1].(string)], state)
	}
	return out, nil
}

func save(ctx context.Context, db *rqlite.Client, broker *notify.Broker, rule Rule, old storedState, next State, now time.Time, reason string) error {
	if !old.LastEvaluated.IsZero() && !now.After(old.LastEvaluated) {
		return nil
	}
	if old.ID == "" {
		old.ID = instanceID(rule, next.Labels)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO alert_evaluations(id,rule_id) VALUES(?,?)`, Params: []interface{}{old.ID, rule.ID}}}); err != nil {
		return err
	}
	guard := ` EXISTS(SELECT 1 FROM alert_evaluations WHERE id=? AND version=?)`
	guardParams := []interface{}{old.ID, old.Version}
	labels, _ := json.Marshal(next.Labels)
	if next.Labels == nil {
		labels = []byte("{}")
	}
	statements := []rqlite.Statement{}
	var notification *notify.Notification
	message := rule.Message
	if message == "" {
		message = rule.Name
	}
	stackID, nodeID := next.Labels["stack_id"], next.Labels["node_id"]
	if stackID == "" {
		stackID = rule.StackID
	}
	if nodeID == "" {
		nodeID = rule.NodeID
	}
	if next.Status == "firing" {
		next.Severity = rule.Severity
		kind := ""
		if next.AlertID == "" {
			next.AlertID = uuid.NewString()
			kind = "alert"
			statements = append(statements, rqlite.Statement{SQL: `INSERT INTO alerts(id,rule_id,stack_id,node_id,severity,kind,state,message,created_at,labels,updated_at) SELECT ?,?,?,?,?,?,'active',?,?,?,? WHERE` + guard, Params: append([]interface{}{next.AlertID, rule.ID, stackID, nodeID, rule.Severity, "evaluator", message, now.Format(time.RFC3339Nano), string(labels), now.Format(time.RFC3339Nano)}, guardParams...)})
		} else {
			if severityRank(rule.Severity) > severityRank(old.Severity) {
				kind = "alert.escalated"
			}
			statements = append(statements, rqlite.Statement{SQL: `UPDATE alerts SET severity=?,message=?,labels=?,updated_at=?,stack_id=?,node_id=? WHERE id=? AND` + guard, Params: append([]interface{}{rule.Severity, message, string(labels), now.Format(time.RFC3339Nano), stackID, nodeID, next.AlertID}, guardParams...)})
		}
		if kind != "" {
			notification = &notify.Notification{ID: uuid.NewString(), AlertID: next.AlertID, StackID: stackID, NodeID: nodeID, Kind: kind, Severity: rule.Severity, Title: rule.Name, Body: message, State: "unread", CreatedAt: now}
		}
	}
	if next.Status == "resolved" && next.AlertID != "" {
		if reason == "" {
			reason = "recovered"
		}
		statements = append(statements,
			rqlite.Statement{SQL: `UPDATE alerts SET state='resolved',resolved_at=?,updated_at=?,resolution_reason=? WHERE id=? AND` + guard, Params: append([]interface{}{now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), reason, next.AlertID}, guardParams...)},
			rqlite.Statement{SQL: `UPDATE notifications SET resolved_at=? WHERE alert_id=? AND` + guard, Params: append([]interface{}{now.Format(time.RFC3339Nano), next.AlertID}, guardParams...)},
		)
		notification = &notify.Notification{ID: uuid.NewString(), AlertID: next.AlertID, StackID: stackID, NodeID: nodeID, Kind: "alert.resolved", Severity: next.Severity, Title: rule.Name, Body: reason, State: "unread", CreatedAt: now}
		next.AlertID = ""
		next.PendingSince, next.RecoverySince = time.Time{}, time.Time{}
	}
	if notification != nil {
		statements = append(statements, rqlite.Statement{SQL: `INSERT INTO notifications(id,alert_id,stack_id,node_id,kind,severity,title,body,state,created_at) SELECT ?,?,?,?,?,?,?,?,?,? WHERE` + guard, Params: append([]interface{}{notification.ID, notification.AlertID, stackID, nodeID, notification.Kind, notification.Severity, notification.Title, notification.Body, notification.State, now.Format(time.RFC3339Nano)}, guardParams...)})
	}
	stateJSON, err := json.Marshal(next)
	if err != nil {
		return err
	}
	statements = append(statements, rqlite.Statement{SQL: `UPDATE alert_evaluations SET state_json=?,version=version+1 WHERE id=? AND version=?`, Params: []interface{}{string(stateJSON), old.ID, old.Version}})
	if err := db.Execute(ctx, statements); err != nil {
		return err
	}
	// Only the winning transaction publishes; persisted notifications survive SSE gaps.
	if broker != nil && notification != nil {
		result, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM notifications WHERE id=?`, Params: []interface{}{notification.ID}})
		if err != nil {
			return err
		}
		if len(result.Values) > 0 {
			body, _ := json.Marshal(notification)
			broker.PublishJSON("notification.new", body)
		}
	}
	return nil
}

func severityRank(severity string) int {
	switch severity {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}
