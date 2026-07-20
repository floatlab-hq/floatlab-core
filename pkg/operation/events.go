package operation

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type Event struct {
	ID          string          `json:"id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	StackID     string          `json:"stack_id,omitempty"`
	Type        string          `json:"type"`
	Outcome     string          `json:"outcome"`
	Actor       string          `json:"actor"`
	OperationID string          `json:"operation_id,omitempty"`
	Containers  json.RawMessage `json:"containers"`
	Details     json.RawMessage `json:"details"`
	Error       string          `json:"error,omitempty"`
}

func RecordEvent(ctx context.Context, db *rqlite.Client, event Event) error {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	if event.Actor == "" {
		event.Actor = "system"
	}
	if len(event.Containers) == 0 {
		event.Containers = json.RawMessage("[]")
	}
	if len(event.Details) == 0 {
		event.Details = json.RawMessage("{}")
	}
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT INTO lifecycle_events(id,occurred_at,stack_id,type,outcome,actor,operation_id,containers,details,error) VALUES(?,?,?,?,?,?,?,?,?,NULLIF(?,''))`,
		Params: []interface{}{event.ID, event.OccurredAt, event.StackID, event.Type, event.Outcome, event.Actor, event.OperationID, string(event.Containers), string(event.Details), event.Error},
	}})
}

func ListEvents(ctx context.Context, db *rqlite.Client, stackID, after string, limit int) ([]Event, error) {
	if limit < 1 || limit > 200 {
		limit = 50
	}
	result, err := db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id,occurred_at,stack_id,type,outcome,actor,operation_id,containers,details,error FROM lifecycle_events
		      WHERE stack_id=? AND (?='' OR (occurred_at,id)>(SELECT occurred_at,id FROM lifecycle_events WHERE id=?))
		      ORDER BY occurred_at,id LIMIT ?`,
		Params: []interface{}{stackID, after, after, limit},
	})
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(result.Values))
	for _, row := range result.Values {
		event := Event{}
		event.ID, _ = row[0].(string)
		if value, ok := row[1].(string); ok {
			event.OccurredAt, _ = time.Parse(time.RFC3339, value)
		}
		event.StackID, _ = row[2].(string)
		event.Type, _ = row[3].(string)
		event.Outcome, _ = row[4].(string)
		event.Actor, _ = row[5].(string)
		event.OperationID, _ = row[6].(string)
		if value, ok := row[7].(string); ok {
			event.Containers = json.RawMessage(value)
		}
		if value, ok := row[8].(string); ok {
			event.Details = json.RawMessage(value)
		}
		event.Error, _ = row[9].(string)
		events = append(events, event)
	}
	return events, nil
}

func Cleanup(ctx context.Context, db *rqlite.Client, before time.Time) error {
	return db.Execute(ctx, []rqlite.Statement{
		{SQL: `DELETE FROM operations WHERE state IN ('succeeded','failed') AND updated_at<?`, Params: []interface{}{before}},
		{SQL: `DELETE FROM lifecycle_events WHERE occurred_at<?`, Params: []interface{}{before}},
	})
}
