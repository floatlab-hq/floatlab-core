package config

import (
	"context"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Migrate creates the config tables if they don't exist.
func Migrate(ctx context.Context, db *rqlite.Client) error {
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `CREATE TABLE IF NOT EXISTS failover_steps (stack_id TEXT NOT NULL, sequence_id TEXT NOT NULL, step INTEGER NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, started_at TEXT, completed_at TEXT, detail TEXT, PRIMARY KEY(stack_id, sequence_id, step))`},
		{SQL: `CREATE TABLE IF NOT EXISTS prefix_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL UNIQUE, stack_id TEXT, created_at TEXT NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS nodes (
			id           TEXT PRIMARY KEY,
			cluster_uuid TEXT NOT NULL,
			name         TEXT NOT NULL UNIQUE,
			addresses    TEXT NOT NULL DEFAULT '[]',
			created_at   DATETIME NOT NULL,
			updated_at   DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS stacks (
			id                   TEXT PRIMARY KEY,
			name                 TEXT NOT NULL UNIQUE,
			icon                 TEXT,
			primary_node_id      TEXT NOT NULL,
			backup_node_id       TEXT,
			compose_yaml         TEXT NOT NULL DEFAULT '',
			zfs_dataset          TEXT NOT NULL DEFAULT '',
			snapshot_schedule    TEXT,
			replication_schedule TEXT,
			backup_schedule      TEXT,
			backup_target        TEXT,
			failover_mode        TEXT NOT NULL DEFAULT 'manual',
			auto_trigger_after   TEXT,
			created_at           DATETIME NOT NULL,
			updated_at           DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS networks (
			id           TEXT PRIMARY KEY,
			name         TEXT NOT NULL UNIQUE,
			prefix       TEXT NOT NULL,
			reserved_min TEXT,
			reserved_max TEXT
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS alert_rules (
			id          TEXT PRIMARY KEY,
			name        TEXT NOT NULL,
			description TEXT,
			type        TEXT NOT NULL,
			condition   TEXT NOT NULL,
			action      TEXT,
			channel     TEXT
		)`},
	}); err != nil {
		return err
	}
	// Preserve legacy columns and records while adding the management contract.
	for table, columns := range map[string][]string{
		"nodes":       {"hostname TEXT NOT NULL DEFAULT ''", "role TEXT NOT NULL DEFAULT 'secondary'", "zfs_pool TEXT NOT NULL DEFAULT 'floatlab'"},
		"networks":    {"stack_id TEXT", "created_at TEXT NOT NULL DEFAULT ''"},
		"alert_rules": {"severity TEXT NOT NULL DEFAULT 'warning'", "stack_id TEXT", "node_id TEXT", "for_duration TEXT NOT NULL DEFAULT ''", "annotations TEXT NOT NULL DEFAULT '{}'", "enabled INTEGER NOT NULL DEFAULT 1", "created_at TEXT NOT NULL DEFAULT ''", "updated_at TEXT NOT NULL DEFAULT ''"},
	} {
		if err := rqlite.AddColumns(ctx, db, table, columns); err != nil {
			return err
		}
	}
	return db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT OR IGNORE INTO prefix_pools(id,name,prefix,stack_id,created_at) SELECT id,name,prefix,stack_id,strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM networks WHERE created_at=''`},
		{SQL: `UPDATE ip_reservations SET prefix_pool=(SELECT id FROM prefix_pools WHERE prefix=ip_reservations.prefix_pool) WHERE EXISTS(SELECT 1 FROM prefix_pools WHERE prefix=ip_reservations.prefix_pool)`},
		{SQL: `UPDATE networks SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE created_at=''`},
		{SQL: `UPDATE alert_rules SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'), updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE created_at=''`},
	})
}
