package rqlite

import "context"

// Migrate ensures all required tables exist.
// Safe to call on every startup (uses CREATE TABLE IF NOT EXISTS).
func Migrate(ctx context.Context, c *Client) error {
	stmts := []Statement{
		{SQL: `CREATE TABLE IF NOT EXISTS tasks (
			id          TEXT PRIMARY KEY,
			type        TEXT NOT NULL,
			stack_id    TEXT,
			payload     TEXT NOT NULL,
			state       TEXT NOT NULL DEFAULT 'pending',
			attempts    INTEGER NOT NULL DEFAULT 0,
			created_at  DATETIME NOT NULL,
			updated_at  DATETIME NOT NULL,
			locked_by   TEXT,
			error       TEXT
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS alerts (
			id             TEXT PRIMARY KEY,
			rule_id        TEXT NOT NULL,
			stack_id       TEXT,
			node_id        TEXT,
			severity       TEXT NOT NULL,
			kind           TEXT NOT NULL,
			state          TEXT NOT NULL DEFAULT 'active',
			message        TEXT NOT NULL,
			silenced_until DATETIME,
			resolved_at    DATETIME,
			created_at     DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS ip_reservations (
			id           TEXT PRIMARY KEY,
			stack_id     TEXT NOT NULL,
			service      TEXT NOT NULL,
			address      TEXT NOT NULL UNIQUE,
			prefix_pool  TEXT NOT NULL,
			allocated_at DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS notifications (
			id          TEXT PRIMARY KEY,
			alert_id    TEXT,
			stack_id    TEXT,
			node_id     TEXT,
			kind        TEXT NOT NULL,
			severity    TEXT NOT NULL,
			title       TEXT NOT NULL,
			body        TEXT NOT NULL,
			state       TEXT NOT NULL DEFAULT 'unread',
			created_at  DATETIME NOT NULL,
			resolved_at DATETIME,
			silenced_until DATETIME
		)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_alerts_state ON alerts(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_notifications_state ON notifications(state)`},
	}
	return c.Execute(ctx, stmts)
}
