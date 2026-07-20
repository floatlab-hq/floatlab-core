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
		{SQL: `CREATE TABLE IF NOT EXISTS operations (
			id          TEXT PRIMARY KEY,
			stack_id    TEXT,
			action      TEXT NOT NULL,
			actor       TEXT NOT NULL,
			state       TEXT NOT NULL DEFAULT 'pending',
			checkpoint  TEXT NOT NULL DEFAULT 'accepted',
			payload     TEXT NOT NULL DEFAULT '{}',
			error       TEXT,
			created_at  DATETIME NOT NULL,
			updated_at  DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS idempotency_keys (
			actor        TEXT NOT NULL,
			key          TEXT NOT NULL,
			method       TEXT NOT NULL,
			path         TEXT NOT NULL,
			request_hash TEXT NOT NULL,
			operation_id TEXT NOT NULL,
			status       INTEGER NOT NULL DEFAULT 0,
			response     TEXT,
			created_at   DATETIME NOT NULL,
			PRIMARY KEY (actor, key)
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS stack_runtime (
			stack_id       TEXT PRIMARY KEY,
			stack_ip       TEXT,
			network_pool   TEXT,
			active_node_id TEXT,
			deleted_at     DATETIME,
			updated_at     DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS network_pools (
			id         TEXT PRIMARY KEY,
			name       TEXT NOT NULL UNIQUE,
			cidr       TEXT NOT NULL,
			start_ip   TEXT NOT NULL,
			end_ip     TEXT NOT NULL,
			is_default INTEGER NOT NULL DEFAULT 0 CHECK(is_default IN (0,1)),
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`},
		{SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_network_pools_default ON network_pools(is_default) WHERE is_default=1`},
		{SQL: `CREATE TABLE IF NOT EXISTS network_allocations (
			id         TEXT PRIMARY KEY,
			pool_id    TEXT NOT NULL,
			stack_id   TEXT NOT NULL UNIQUE,
			address    TEXT NOT NULL,
			state      TEXT NOT NULL CHECK(state IN ('pending','active')),
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL,
			UNIQUE(pool_id,address)
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS lifecycle_events (
			id            TEXT PRIMARY KEY,
			occurred_at   DATETIME NOT NULL,
			stack_id      TEXT,
			type          TEXT NOT NULL,
			outcome       TEXT NOT NULL,
			actor         TEXT NOT NULL,
			operation_id  TEXT,
			containers    TEXT NOT NULL DEFAULT '[]',
			details       TEXT NOT NULL DEFAULT '{}',
			error         TEXT
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS stack_alert_rules (
			id          TEXT PRIMARY KEY,
			stack_id    TEXT NOT NULL,
			name        TEXT NOT NULL,
			metric      TEXT NOT NULL,
			selector    TEXT,
			comparator  TEXT NOT NULL,
			threshold   REAL NOT NULL,
			duration    TEXT NOT NULL,
			severity    TEXT NOT NULL,
			message     TEXT NOT NULL,
			active      INTEGER NOT NULL DEFAULT 1,
			UNIQUE(stack_id,name)
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS alert_status (
			rule_id       TEXT PRIMARY KEY,
			state         TEXT NOT NULL CHECK(state IN ('pending','firing','resolved')),
			observed      REAL,
			observed_at   DATETIME NOT NULL,
			updated_at    DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS stack_snapshots (
			id            TEXT PRIMARY KEY,
			stack_id      TEXT NOT NULL,
			operation_id  TEXT,
			zfs_name      TEXT NOT NULL,
			kind          TEXT NOT NULL,
			created_at    DATETIME NOT NULL,
			UNIQUE(stack_id,zfs_name)
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS recovery_points (
			id             TEXT PRIMARY KEY,
			stack_id       TEXT NOT NULL,
			snapshot_id    TEXT NOT NULL,
			dataset_path   TEXT NOT NULL,
			compose_yaml   TEXT NOT NULL,
			created_at     DATETIME NOT NULL,
			deleted_at     DATETIME
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS scheduler_state (
			stack_id    TEXT NOT NULL,
			tier        TEXT NOT NULL,
			last_run_at DATETIME,
			PRIMARY KEY(stack_id,tier)
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS dns_outbox (
			id          TEXT PRIMARY KEY,
			stack_id    TEXT NOT NULL,
			type        TEXT NOT NULL,
			payload     TEXT NOT NULL,
			created_at  DATETIME NOT NULL,
			acknowledged_at DATETIME
		)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_alerts_state ON alerts(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_notifications_state ON notifications(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_operations_state ON operations(state)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_operations_stack ON operations(stack_id, created_at)`},
		{SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_operations_active_stack ON operations(stack_id) WHERE stack_id IS NOT NULL AND stack_id != '' AND state IN ('pending','running')`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_events_stack_cursor ON lifecycle_events(stack_id, occurred_at, id)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_events_cleanup ON lifecycle_events(occurred_at)`},
		{SQL: `CREATE INDEX IF NOT EXISTS idx_snapshots_stack ON stack_snapshots(stack_id, created_at)`},
		{SQL: `CREATE UNIQUE INDEX IF NOT EXISTS idx_recovery_stack_snapshot ON recovery_points(stack_id,snapshot_id)`},
	}
	return c.Execute(ctx, stmts)
}
