package config

import (
	"context"
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/floatlab/floatlab-core/pkg/alerts"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Migrate creates the config tables if they don't exist.
func Migrate(ctx context.Context, db *rqlite.Client) error {
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `CREATE TABLE IF NOT EXISTS stack_move_grants (operation_id TEXT PRIMARY KEY, stack_id TEXT NOT NULL, payload_hash TEXT NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS workload_move_owners (stack_id TEXT PRIMARY KEY, node_id TEXT NOT NULL, generation INTEGER NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS workload_move_receipts (operation_id TEXT PRIMARY KEY, stack_id TEXT NOT NULL, phase TEXT NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS workload_move_reservations (operation_id TEXT PRIMARY KEY, stack_id TEXT NOT NULL UNIQUE, name TEXT NOT NULL UNIQUE)`},
		{SQL: `CREATE TABLE IF NOT EXISTS failover_steps (stack_id TEXT NOT NULL, sequence_id TEXT NOT NULL, step INTEGER NOT NULL, name TEXT NOT NULL, state TEXT NOT NULL, started_at TEXT, completed_at TEXT, detail TEXT, PRIMARY KEY(stack_id, sequence_id, step))`},
		{SQL: `CREATE TABLE IF NOT EXISTS prefix_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, prefix TEXT NOT NULL UNIQUE, stack_id TEXT, created_at TEXT NOT NULL)`},
		{SQL: `CREATE TABLE IF NOT EXISTS node_pool_ranges (pool_id TEXT PRIMARY KEY, start INTEGER NOT NULL, end INTEGER NOT NULL, owner TEXT NOT NULL, CHECK(start<=end))`},
		{SQL: `CREATE TRIGGER IF NOT EXISTS node_pool_range_insert BEFORE INSERT ON node_pool_ranges BEGIN SELECT RAISE(ABORT,'address range overlaps a reserved node range') WHERE EXISTS(SELECT 1 FROM node_pool_ranges WHERE pool_id<>NEW.pool_id AND start<=NEW.end AND end>=NEW.start); END`},
		{SQL: `CREATE TRIGGER IF NOT EXISTS node_pool_range_update BEFORE UPDATE ON node_pool_ranges BEGIN SELECT RAISE(ABORT,'range ownership cannot be transferred implicitly') WHERE OLD.owner<>NEW.owner; SELECT RAISE(ABORT,'address range overlaps a reserved node range') WHERE EXISTS(SELECT 1 FROM node_pool_ranges WHERE pool_id<>NEW.pool_id AND start<=NEW.end AND end>=NEW.start); END`},
		{SQL: `CREATE TABLE IF NOT EXISTS nodes (
			id           TEXT PRIMARY KEY,
			cluster_uuid TEXT NOT NULL,
			name         TEXT NOT NULL UNIQUE,
			addresses    TEXT NOT NULL DEFAULT '[]',
			created_at   DATETIME NOT NULL,
			updated_at   DATETIME NOT NULL
		)`},
		{SQL: `CREATE TABLE IF NOT EXISTS backup_targets (stack_id TEXT NOT NULL,dataset TEXT NOT NULL,node_id TEXT NOT NULL,source_node_id TEXT NOT NULL, schedule TEXT NOT NULL,local_policy TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,cleanup_pending INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',updated_at TEXT NOT NULL DEFAULT '',PRIMARY KEY(stack_id,dataset,node_id))`},
		{SQL: `CREATE TABLE IF NOT EXISTS removed_backup_nodes(node_id TEXT PRIMARY KEY)`},
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
		{SQL: `CREATE TRIGGER IF NOT EXISTS workload_move_reserved_name BEFORE INSERT ON stacks
			WHEN EXISTS(SELECT 1 FROM workload_move_reservations WHERE name=NEW.name AND stack_id<>NEW.id)
			BEGIN SELECT RAISE(ABORT,'stack name is reserved by a move'); END`},
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
	// Reservations survive pool deletion and member removal until explicit retirement.
	ranges, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT p.id,p.start_ip,p.end_ip,COALESCE((SELECT MIN(node_id) FROM network_pool_nodes WHERE pool_id=p.id),'') FROM network_pools p WHERE NOT EXISTS(SELECT 1 FROM node_pool_ranges WHERE pool_id=p.id)`})
	if err != nil {
		return err
	}
	var reservations []rqlite.Statement
	for _, row := range ranges.Values {
		start, e1 := netip.ParseAddr(fmt.Sprint(row[1]))
		end, e2 := netip.ParseAddr(fmt.Sprint(row[2]))
		if e1 != nil || e2 != nil || !start.Is4() || !end.Is4() {
			return fmt.Errorf("invalid existing pool range %v", row[0])
		}
		reservations = append(reservations, rqlite.Statement{SQL: `INSERT INTO node_pool_ranges(pool_id,start,end,owner) VALUES(?,?,?,?) ON CONFLICT(pool_id) DO NOTHING`, Params: []any{row[0], binary.BigEndian.Uint32(start.AsSlice()), binary.BigEndian.Uint32(end.AsSlice()), row[3]}})
	}
	if err = db.Execute(ctx, reservations); err != nil {
		return err
	}
	// Preserve legacy columns and records while adding the management contract.
	for table, columns := range map[string][]string{
		"backup_targets": {"origin TEXT NOT NULL DEFAULT 'api'", "generation INTEGER NOT NULL DEFAULT 1"},
		"nodes":          {"hostname TEXT NOT NULL DEFAULT ''", "role TEXT NOT NULL DEFAULT 'secondary'", "zfs_pool TEXT NOT NULL DEFAULT 'floatlab'"},
		"networks":       {"stack_id TEXT", "created_at TEXT NOT NULL DEFAULT ''"},
		"alert_rules":    {"severity TEXT NOT NULL DEFAULT 'warning'", "stack_id TEXT", "node_id TEXT", "for_duration TEXT NOT NULL DEFAULT ''", "annotations TEXT NOT NULL DEFAULT '{}'", "enabled INTEGER NOT NULL DEFAULT 1", "created_at TEXT NOT NULL DEFAULT ''", "updated_at TEXT NOT NULL DEFAULT ''"},
	} {
		if err := rqlite.AddColumns(ctx, db, table, columns); err != nil {
			return err
		}
	}
	if err := rqlite.AddColumns(ctx, db, "alert_rules", []string{
		"recovery_condition TEXT NOT NULL DEFAULT ''",
		"recovery_duration TEXT NOT NULL DEFAULT '1m'",
		"max_sample_age TEXT NOT NULL DEFAULT '2m'",
		"freshness_query TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		return err
	}
	if err := alerts.Migrate(ctx, db); err != nil {
		return err
	}
	return db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT OR IGNORE INTO prefix_pools(id,name,prefix,stack_id,created_at) SELECT id,name,prefix,stack_id,strftime('%Y-%m-%dT%H:%M:%SZ','now') FROM networks WHERE created_at=''`},
		{SQL: `UPDATE ip_reservations SET prefix_pool=(SELECT id FROM prefix_pools WHERE prefix=ip_reservations.prefix_pool) WHERE EXISTS(SELECT 1 FROM prefix_pools WHERE prefix=ip_reservations.prefix_pool)`},
		{SQL: `UPDATE networks SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE created_at=''`},
		{SQL: `UPDATE alert_rules SET created_at=strftime('%Y-%m-%dT%H:%M:%SZ','now'), updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now') WHERE created_at=''`},
	})
}
