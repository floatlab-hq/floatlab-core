package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
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

func save(ctx context.Context, db *rqlite.Client, broker *notify.Broker, rule Rule, old storedState, next State, now time.Time, reason, observer string) error {
	if !old.LastEvaluated.IsZero() && !now.After(old.LastEvaluated) {
		return nil
	}
	if old.ID == "" {
		old.ID = instanceID(rule, next.Labels)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO alert_evaluations(id,rule_id) VALUES(?,?)`, Params: []interface{}{old.ID, rule.ID}}}); err != nil {
		return err
	}
	status, action := "unknown", "keep"
	switch next.Status {
	case "pending":
		status = "failing"
	case "inactive", "resolved":
		status = "healthy"
	}
	if next.Status == "firing" {
		status = "failing"
		action = "open"
		if !next.RecoverySince.IsZero() {
			action = "keep"
			status = "healthy"
		}
	}
	if next.Status == "resolved" {
		action = "resolve"
	}
	if reason == "rule disabled or removed" {
		status = "disabled"
		action = "retire"
	}
	message := rule.Message
	if message == "" {
		message = rule.Name
	}
	if reason != "" {
		message = reason
	}
	severity := rule.Severity
	if severity == "" {
		severity = old.Severity
	}
	if severity == "" {
		severity = "warning"
	}
	stackID, nodeID := next.Labels["stack_id"], next.Labels["node_id"]
	if stackID == "" {
		stackID = rule.StackID
	}
	if nodeID == "" {
		nodeID = rule.NodeID
	}
	result := monitoring.Result{CheckID: "metrics:" + old.ID, Producer: "metrics", Kind: "evaluator", Observer: observer, Resource: monitoring.Resource{HostID: nodeID, WorkloadID: stackID, Pool: next.Labels["pool"], Dataset: next.Labels["dataset"], SourceID: next.Labels["source_node_id"], DestinationID: next.Labels["dest_node_id"]}, Status: status, Reason: reason, ObservedAt: next.LastObserved, EvaluatedAt: now, Labels: next.Labels, Evidence: map[string]interface{}{"value": next.Value, "error": next.Error}}
	if rule.MaxAge > 0 && !next.LastObserved.IsZero() {
		result.ExpiresAt = next.LastObserved.Add(rule.MaxAge)
	}
	// A repeated sample keeps the active incident but cannot establish recovery.
	if action == "open" && (result.ObservedAt.IsZero() || result.ExpiresAt.Before(now)) {
		action = "keep"
	}
	return monitoring.Record(ctx, db, broker, monitoring.Transition{Result: result, RuleID: rule.ID, Title: rule.Name, Message: message, Severity: severity, Action: action, Notify: true,
		Guard: `EXISTS(SELECT 1 FROM alert_evaluations WHERE id=? AND version=?)`, GuardParams: []interface{}{old.ID, old.Version},
		Extra: func(incidentID string, guard string, params []interface{}) ([]rqlite.Statement, error) {
			next.AlertID = incidentID
			if action == "open" {
				next.Severity = severity
			}
			if action == "resolve" || action == "retire" {
				next.PendingSince, next.RecoverySince = time.Time{}, time.Time{}
			}
			data, err := json.Marshal(next)
			if err != nil {
				return nil, err
			}
			return []rqlite.Statement{{SQL: `UPDATE alert_evaluations SET state_json=?,version=version+1 WHERE id=? AND ` + guard, Params: append([]interface{}{string(data), old.ID}, params...)}}, nil
		}})
}
