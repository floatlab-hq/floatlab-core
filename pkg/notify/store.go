package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Notification mirrors the notifications rqlite table.
type Notification struct {
	ID         string     `json:"id"`
	AlertID    string     `json:"alert_id,omitempty"`
	StackID    string     `json:"stack_id,omitempty"`
	NodeID     string     `json:"node_id,omitempty"`
	Kind       string     `json:"kind"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Body       string     `json:"body"`
	State      string     `json:"state"` // "unread" | "read"
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// Create persists n to rqlite and publishes it to broker.
func Create(ctx context.Context, db *rqlite.Client, broker *Broker, n *Notification) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	if n.State == "" {
		n.State = "unread"
	}
	n.CreatedAt = time.Now().UTC()

	if err := db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO notifications(id, alert_id, stack_id, node_id, kind, severity, title, body, state, created_at)
		      VALUES(?,?,?,?,?,?,?,?,?,?)`,
		Params: []interface{}{
			n.ID, n.AlertID, n.StackID, n.NodeID, n.Kind, n.Severity,
			n.Title, n.Body, n.State, n.CreatedAt,
		},
	}}); err != nil {
		return fmt.Errorf("notify: create: %w", err)
	}

	if broker != nil {
		if b, err := json.Marshal(n); err == nil {
			broker.PublishJSON("notification.new", b)
		}
	}
	return nil
}

// Silence sets silenced_until on a notification row.
func Silence(ctx context.Context, db *rqlite.Client, id string, until time.Time) error {
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE notifications SET silenced_until = ? WHERE id = ?`,
		Params: []interface{}{until.UTC(), id},
	}})
}

// Cleanup deletes resolved/read notifications older than 30 days.
func Cleanup(ctx context.Context, db *rqlite.Client) error {
	cutoff := time.Now().UTC().Add(-30 * 24 * time.Hour)
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM notifications WHERE state = 'read' AND created_at < ?`,
		Params: []interface{}{cutoff},
	}})
}
