package monitoring

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Execution identifies one scheduled occurrence; retries use separate Attempt IDs.
type Execution struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Resource       Resource   `json:"resource"`
	State          string     `json:"state"`
	Active         bool       `json:"active"`
	WasMissed      bool       `json:"was_missed"`
	WasOverdue     bool       `json:"was_overdue"`
	ScheduledAt    time.Time  `json:"scheduled_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	LastProgressAt *time.Time `json:"last_progress_at,omitempty"`
	Reason         string     `json:"reason,omitempty"`
}

type Attempt struct {
	ID          string     `json:"id"`
	ExecutionID string     `json:"execution_id"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	Result      string     `json:"result,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

// Delivery is a device delivery attempt, independent of the persistent inbox event.
type Delivery struct {
	ID             string     `json:"id"`
	NotificationID string     `json:"notification_id"`
	SubscriptionID string     `json:"subscription_id"`
	State          string     `json:"state"` // pending, delivered, failed
	AttemptedAt    *time.Time `json:"attempted_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	Error          string     `json:"error,omitempty"`
}

type Action struct {
	ID            string   `json:"id"`
	Impact        string   `json:"impact"`
	Prerequisites []string `json:"prerequisites"`
}

// Intervention records an offer; persisting it never executes an action.
type Intervention struct {
	ID          string    `json:"id"`
	IncidentID  string    `json:"incident_id"`
	State       string    `json:"state"` // pending, executing, succeeded, failed, expired, cancelled
	Actions     []Action  `json:"actions"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Actor       string    `json:"actor,omitempty"`
	OperationID string    `json:"operation_id,omitempty"`
}

func SaveExecution(ctx context.Context, db *rqlite.Client, e Execution) error {
	if e.ID == "" || e.Kind == "" || e.ScheduledAt.IsZero() {
		return fmt.Errorf("monitoring: execution identity, kind and scheduled time required")
	}
	for _, at := range []*time.Time{e.StartedAt, e.CompletedAt, e.LastProgressAt} {
		if at != nil && at.IsZero() {
			return fmt.Errorf("monitoring: execution timestamps must be nonzero when present")
		}
	}
	switch e.State {
	case "scheduled", "missed":
		if e.Active || e.StartedAt != nil || e.CompletedAt != nil {
			return fmt.Errorf("monitoring: unstarted execution cannot be active")
		}
	case "running", "overdue":
		if !e.Active || e.StartedAt == nil || e.CompletedAt != nil {
			return fmt.Errorf("monitoring: active execution requires start without completion")
		}
	case "succeeded", "failed":
		if e.Active || e.StartedAt == nil || e.CompletedAt == nil {
			return fmt.Errorf("monitoring: finished execution requires start and completion")
		}
	default:
		return fmt.Errorf("monitoring: invalid execution state")
	}
	if e.StartedAt != nil && e.CompletedAt != nil && e.CompletedAt.Before(*e.StartedAt) {
		return fmt.Errorf("monitoring: execution completed before start")
	}
	e.WasMissed = e.WasMissed || e.State == "missed"
	e.WasOverdue = e.WasOverdue || e.State == "overdue"
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT state,record_json FROM monitoring_executions WHERE id=?`, Params: []interface{}{e.ID}})
	if err != nil {
		return err
	}
	if len(rows.Values) == 0 {
		result, err := db.Request(ctx, rqlite.Statement{SQL: `INSERT OR IGNORE INTO monitoring_executions(id,state,active,record_json) VALUES(?,?,?,?)`, Params: []interface{}{e.ID, e.State, e.Active, string(data)}})
		if err != nil {
			return err
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("monitoring: execution concurrently created")
		}
		return nil
	}
	oldState := rows.Values[0][0].(string)
	oldJSON := rows.Values[0][1].(string)
	var old Execution
	if err := json.Unmarshal([]byte(oldJSON), &old); err != nil {
		return err
	}
	e.WasMissed = e.WasMissed || old.WasMissed
	e.WasOverdue = e.WasOverdue || old.WasOverdue
	data, err = json.Marshal(e)
	if err != nil {
		return err
	}
	if oldJSON == string(data) {
		return nil
	}
	if oldState == "succeeded" || oldState == "failed" {
		return fmt.Errorf("monitoring: completed occurrence is immutable; create another occurrence")
	}
	if old.Kind != e.Kind || old.Resource != e.Resource || !old.ScheduledAt.Equal(e.ScheduledAt) {
		return fmt.Errorf("monitoring: occurrence identity cannot change")
	}
	if old.StartedAt != nil && (e.StartedAt == nil || !old.StartedAt.Equal(*e.StartedAt)) {
		return fmt.Errorf("monitoring: execution start cannot change")
	}
	if old.LastProgressAt != nil && (e.LastProgressAt == nil || e.LastProgressAt.Before(*old.LastProgressAt)) {
		return fmt.Errorf("monitoring: execution progress cannot move backwards")
	}
	result, err := db.Request(ctx, rqlite.Statement{SQL: `UPDATE monitoring_executions SET state=?,active=?,record_json=? WHERE id=? AND record_json=?`, Params: []interface{}{e.State, e.Active, string(data), e.ID, oldJSON}})
	if err != nil {
		return err
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("monitoring: execution concurrently updated")
	}
	return nil
}

func SaveAttempt(ctx context.Context, db *rqlite.Client, a Attempt) error {
	if a.ID == "" || a.ExecutionID == "" || a.StartedAt.IsZero() {
		return fmt.Errorf("monitoring: attempt identity, occurrence and start required")
	}
	if a.CompletedAt != nil && a.CompletedAt.IsZero() {
		return fmt.Errorf("monitoring: completion timestamp must be nonzero")
	}
	if a.CompletedAt != nil && a.CompletedAt.Before(a.StartedAt) {
		return fmt.Errorf("monitoring: attempt completed before start")
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT record_json FROM monitoring_attempts WHERE id=?`, Params: []interface{}{a.ID}})
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		var old Attempt
		if err := json.Unmarshal([]byte(rows.Values[0][0].(string)), &old); err != nil {
			return err
		}
		if rows.Values[0][0] == string(data) {
			return nil
		}
		if old.CompletedAt != nil || old.ExecutionID != a.ExecutionID || !old.StartedAt.Equal(a.StartedAt) {
			return fmt.Errorf("monitoring: attempt identity and completed result are immutable")
		}
		result, err := db.Request(ctx, rqlite.Statement{SQL: `UPDATE monitoring_attempts SET record_json=? WHERE id=? AND record_json=?`, Params: []interface{}{string(data), a.ID, rows.Values[0][0]}})
		if err != nil {
			return err
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("monitoring: attempt concurrently updated")
		}
		return nil
	}
	result, err := db.Request(ctx, rqlite.Statement{SQL: `INSERT OR IGNORE INTO monitoring_attempts(id,execution_id,record_json) SELECT ?,?,? WHERE EXISTS(SELECT 1 FROM monitoring_executions WHERE id=?)`, Params: []interface{}{a.ID, a.ExecutionID, string(data), a.ExecutionID}})
	if err != nil {
		return err
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("monitoring: occurrence missing or attempt concurrently created")
	}
	return nil
}
