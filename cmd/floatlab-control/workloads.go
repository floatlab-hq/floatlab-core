package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/floatlab/floatlab-core/internal/control"
	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/lab"
	"github.com/floatlab/floatlab-core/pkg/config"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	runstate "github.com/floatlab/floatlab-core/pkg/run"
)

var labCacheTables = []string{"nodes", "users", "user_credentials", "user_recovery_codes", "networks", "prefix_pools", "network_pools", "network_pool_nodes", "network_pool_scope", "node_pool_ranges", "alert_rules"}

var workloadTables = []string{"stacks", "stack_runtime", "tasks", "operations", "idempotency_keys", "ip_reservations", "network_allocations", "lifecycle_events", "stack_alert_rules", "alert_status", "stack_snapshots", "recovery_points", "scheduler_state", "dns_outbox", "notifications", "alerts", "alert_evaluations", "failover_steps"}

type workloadBackup struct {
	NodeID string                `json:"node_id"`
	Tables []string              `json:"tables"`
	Rows   []*rqlite.QueryResult `json:"rows"`
}

func initializeLab(ctx context.Context, db *rqlite.Client) error {
	_, authErr := os.Stat(filepath.Join(labDataDir, "auth.json"))
	if authErr != nil && !os.IsNotExist(authErr) {
		return fmt.Errorf("inspect joined lab authentication: %w", authErr)
	}
	// Imported lab credentials retire this appliance's standalone bootstrap.
	bootstrapAllowed := os.IsNotExist(authErr)
	if bootstrapAllowed && (bootstrapUser == "") != (bootstrapPass == "") {
		return fmt.Errorf("bootstrap user and password must be configured together")
	}
	if err := rqlite.Migrate(ctx, db); err != nil {
		return err
	}
	if err := config.Migrate(ctx, db); err != nil {
		return err
	}
	if bootstrapAllowed && bootstrapUser != "" {
		if err := control.BootstrapUser(ctx, db, bootstrapUser, bootstrapPass); err != nil {
			return err
		}
	}
	if bootstrapAllowed && bootstrapFile != "" {
		if err := control.BootstrapFile(ctx, db, bootstrapFile); err != nil {
			return err
		}
	}
	if registerLocalNode {
		return control.RegisterApplianceNode(ctx, db, hostNodeID, localNodeName())
	}
	return nil
}

func importLifecycle(ctx context.Context, db *rqlite.Client, log *zap.Logger) error {
	done, err := db.LocalMigrationDone(ctx, "legacy-lifecycle")
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	statements := []rqlite.Statement{{SQL: `CREATE TABLE IF NOT EXISTS workload_lifecycle (stack_id TEXT PRIMARY KEY, entry TEXT NOT NULL)`}}
	if _, err := os.Stat(filepath.Join(raftDataDir, "raft-log.bolt")); err == nil {
		if err = lab.BackupDirectory(raftDataDir, filepath.Join(filepath.Dir(workloadDBPath), "legacy-raft-backup")); err != nil {
			return fmt.Errorf("backup legacy lifecycle: %w", err)
		}
		legacy, err := floatraft.NewNode(floatraft.Config{NodeID: raftNodeID, BindAddr: raftBindAddr, AdvertiseAddr: raftAdvAddr, DataDir: raftDataDir, Bootstrap: raftBootstrap}, log)
		if err != nil {
			return fmt.Errorf("open legacy lifecycle: %w", err)
		}
		defer legacy.Shutdown()
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for !legacy.IsLeader() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-deadline.C:
				return fmt.Errorf("legacy lifecycle migration could not establish standalone authority")
			case <-tick.C:
			}
		}
		if err = legacy.Barrier(5 * time.Second); err != nil {
			return err
		}
		for id, state := range legacy.FSM().AllStates() {
			b, err := json.Marshal(runstate.StackStateChanged{StackID: id, NodeID: hostNodeID, To: state.State, Timestamp: state.UpdatedAt})
			if err != nil {
				return err
			}
			statements = append(statements, rqlite.Statement{SQL: `INSERT INTO workload_lifecycle(stack_id,entry) VALUES(?,?)`, Params: []any{id, string(b)}})
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	statements = append(statements, rqlite.Statement{SQL: `INSERT INTO local_migrations(name,applied_at) VALUES('legacy-lifecycle',?)`, Params: []any{time.Now().UTC().Format(time.RFC3339Nano)}})
	return db.Execute(ctx, statements)
}

func openWorkloads(ctx context.Context, labDB *rqlite.Client) (*rqlite.Client, error) {
	db, err := rqlite.OpenLocal(workloadDBPath, hostNodeID)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*rqlite.Client, error) { db.Close(); return nil, err }
	if err = rqlite.Migrate(ctx, db); err != nil {
		return fail(err)
	}
	if err = config.Migrate(ctx, db); err != nil {
		return fail(err)
	}
	done, err := db.LocalMigrationDone(ctx, "legacy-rqlite")
	if err != nil {
		return fail(err)
	}
	if !done {
		// Only this owner's records may cross the standalone migration boundary.
		rows, e := labDB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM stacks WHERE primary_node_id<>?`, Params: []any{hostNodeID}})
		if e != nil {
			return fail(fmt.Errorf("initial workload migration needs lab database: %w", e))
		}
		if len(rows.Values) > 0 {
			return fail(fmt.Errorf("explicit per-owner migration required for existing multi-node workload records"))
		}
		queries := make([]rqlite.Statement, len(workloadTables))
		for i, table := range workloadTables {
			queries[i] = rqlite.Statement{SQL: `SELECT * FROM ` + table}
		}
		backup, e := labDB.QueryBatch(ctx, queries)
		if e != nil {
			return fail(fmt.Errorf("backup legacy workload rows: %w", e))
		}
		backupPath := filepath.Join(filepath.Dir(workloadDBPath), "legacy-rqlite-backup.json")
		if _, e = os.Lstat(backupPath); os.IsNotExist(e) {
			e = credentials.WritePrivate(backupPath, workloadBackup{NodeID: hostNodeID, Tables: workloadTables, Rows: backup})
		}
		if e != nil {
			return fail(e)
		}
		var verified workloadBackup
		if e = credentials.ReadPrivate(backupPath, &verified); e != nil || verified.NodeID != hostNodeID || !slices.Equal(verified.Tables, workloadTables) || len(verified.Rows) != len(workloadTables) {
			return fail(fmt.Errorf("invalid legacy workload backup: %v", e))
		}
		for _, table := range verified.Rows {
			if table == nil {
				return fail(fmt.Errorf("incomplete legacy workload backup"))
			}
		}
		if err = rqlite.ImportLocal(ctx, labDB, db, workloadTables); err != nil {
			return fail(err)
		}
	}
	return db, nil
}

func cacheLab(ctx context.Context, labDB, local *rqlite.Client) error {
	if err := rqlite.CopyTables(ctx, labDB, local, labCacheTables); err != nil {
		return err
	}
	rows, err := local.Query(ctx, rqlite.Statement{SQL: `SELECT id FROM nodes WHERE id=?`, Params: []any{hostNodeID}})
	if err != nil {
		return err
	}
	if len(rows.Values) > 0 {
		return nil
	}
	return control.RegisterApplianceNode(ctx, local, hostNodeID, localNodeName())
}

func localNodeName() string {
	var identity struct {
		NodeName string `json:"node_name"`
	}
	if applianceConfig != "" && credentials.ReadPrivate(applianceConfig, &identity) == nil && identity.NodeName != "" {
		return identity.NodeName
	}
	return hostNodeID
}
