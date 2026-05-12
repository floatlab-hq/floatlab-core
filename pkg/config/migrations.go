package config

import (
	"context"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Migrate creates the config tables if they don't exist.
func Migrate(ctx context.Context, db *rqlite.Client) error {
	return db.Execute(ctx, []rqlite.Statement{
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
	})
}
