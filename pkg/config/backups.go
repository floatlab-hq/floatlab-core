package config

import (
	"context"
	"errors"
	"fmt"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"strconv"
	"strings"
	"time"
)

var ErrBackupTargetConflict = errors.New("backup target cannot be configured until removal cleanup finishes")

type BackupTarget struct {
	Generation     int64  `json:"generation"`
	Revision       string `json:"revision"`
	Origin         string `json:"origin,omitempty"`
	StackID        string `json:"stack_id"`
	Dataset        string `json:"dataset"`
	NodeID         string `json:"node_id"`
	SourceNodeID   string `json:"source_node_id"`
	Schedule       string `json:"schedule"`
	LocalPolicy    string `json:"local_policy"`
	Enabled        bool   `json:"enabled"`
	CleanupPending bool   `json:"cleanup_pending"`
	Error          string `json:"error,omitempty"`
}

func MountPolicy(m compose.Mount) string {
	var tokens []string
	for _, tier := range m.Snapshots {
		tokens = append(tokens, fmt.Sprintf("%s/%d", tier.Interval, tier.Retain))
	}
	return strings.Join(tokens, " ")
}

func (s *Store) BackupTargets(ctx context.Context, stackID string) ([]BackupTarget, error) {
	query := `SELECT stack_id,dataset,node_id,source_node_id,schedule,local_policy,enabled,cleanup_pending,error,origin,updated_at,generation FROM backup_targets`
	var params []interface{}
	if stackID != "" {
		query += ` WHERE stack_id=?`
		params = append(params, stackID)
	}
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: query + ` ORDER BY stack_id,dataset,node_id`, Params: params})
	if err != nil {
		return nil, err
	}
	out := []BackupTarget{}
	for _, r := range rows.Values {
		generation, _ := strconv.ParseInt(fmt.Sprint(r[11]), 10, 64)
		out = append(out, BackupTarget{Generation: generation, Revision: fmt.Sprint(r[10]), Origin: fmt.Sprint(r[9]), StackID: fmt.Sprint(r[0]), Dataset: fmt.Sprint(r[1]), NodeID: fmt.Sprint(r[2]), SourceNodeID: fmt.Sprint(r[3]), Schedule: fmt.Sprint(r[4]), LocalPolicy: fmt.Sprint(r[5]), Enabled: fmt.Sprint(r[6]) == "1", CleanupPending: fmt.Sprint(r[7]) == "1", Error: fmt.Sprint(r[8])})
	}
	return out, nil
}

func (s *Store) PutBackupTarget(ctx context.Context, t BackupTarget) error {
	removed, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT node_id FROM removed_backup_nodes WHERE node_id=?`, Params: []interface{}{t.NodeID}})
	if err != nil {
		return err
	}
	if len(removed.Values) > 0 {
		return ErrBackupTargetConflict
	}
	pending, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT cleanup_pending,source_node_id,enabled FROM backup_targets WHERE stack_id=? AND dataset=? AND node_id=?`, Params: []interface{}{t.StackID, t.Dataset, t.NodeID}})
	if err != nil {
		return err
	}
	if len(pending.Values) > 0 {
		row := pending.Values[0]
		if fmt.Sprint(row[0]) == "1" || (fmt.Sprint(row[2]) == "1" && fmt.Sprint(row[1]) != t.SourceNodeID) {
			return ErrBackupTargetConflict
		}
	}
	if t.Origin == "" {
		t.Origin = "api"
	}
	result, err := s.db.Request(ctx, rqlite.Statement{SQL: `INSERT INTO backup_targets(stack_id,dataset,node_id,source_node_id,schedule,local_policy,updated_at,origin) SELECT ?,?,?,?,?,?,?,? WHERE NOT EXISTS(SELECT 1 FROM removed_backup_nodes WHERE node_id=?) ON CONFLICT(stack_id,dataset,node_id) DO UPDATE SET generation=backup_targets.generation+CASE WHEN backup_targets.schedule<>excluded.schedule OR backup_targets.source_node_id<>excluded.source_node_id OR backup_targets.enabled=0 THEN 1 ELSE 0 END,source_node_id=excluded.source_node_id,schedule=excluded.schedule,local_policy=excluded.local_policy,origin=excluded.origin,enabled=1,cleanup_pending=0,error='',updated_at=excluded.updated_at WHERE backup_targets.cleanup_pending=0 AND (backup_targets.source_node_id=excluded.source_node_id OR backup_targets.enabled=0)`, Params: []interface{}{t.StackID, t.Dataset, t.NodeID, t.SourceNodeID, t.Schedule, t.LocalPolicy, time.Now().UTC().Format(time.RFC3339Nano), t.Origin, t.NodeID}})
	if err != nil {
		return err
	}
	if result.RowsAffected == 0 {
		return ErrBackupTargetConflict
	}
	return nil
}

func (s *Store) RemoveBackupTarget(ctx context.Context, stackID, dataset, nodeID string) error {
	return s.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE backup_targets SET enabled=0,generation=generation+1,cleanup_pending=1,error='',updated_at=? WHERE stack_id=? AND dataset=? AND node_id=? AND enabled=1`, Params: []interface{}{time.Now().UTC().Format(time.RFC3339Nano), stackID, dataset, nodeID}}})
}

// SyncBackupTargets commits a new Compose configuration without forgetting pending cleanup.
func (s *Store) SyncBackupTargets(ctx context.Context, st *Stack) error {
	spec, err := compose.ParseLifecycle(st.ComposeYAML, st.Name)
	if err != nil {
		return nil
	}
	existing, err := s.BackupTargets(ctx, st.ID)
	if err != nil {
		return err
	}
	desired := map[string]bool{}
	for _, mount := range spec.Mounts {
		if err := s.db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE backup_targets SET local_policy=? WHERE stack_id=? AND dataset=?`, Params: []interface{}{MountPolicy(mount), st.ID, mount.Dataset}}}); err != nil {
			return err
		}
		for _, b := range mount.Backups {
			if b.NodeID == st.PrimaryNodeID {
				return fmt.Errorf("backup destination cannot be the source node")
			}
			desired[mount.Dataset+"\x00"+b.NodeID] = true
			if err := s.PutBackupTarget(ctx, BackupTarget{Origin: "compose", StackID: st.ID, Dataset: mount.Dataset, NodeID: b.NodeID, SourceNodeID: st.PrimaryNodeID, Schedule: b.Schedule, LocalPolicy: MountPolicy(mount)}); err != nil {
				return err
			}
		}
	}
	for _, t := range existing {
		if t.Origin == "compose" && !desired[t.Dataset+"\x00"+t.NodeID] {
			if err := s.RemoveBackupTarget(ctx, t.StackID, t.Dataset, t.NodeID); err != nil {
				return err
			}
		}
	}
	return nil
}

// DisableBackupNode is called before applying the membership tombstone locally.
func (s *Store) DisableBackupNode(ctx context.Context, nodeID string) error {
	return s.db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT OR IGNORE INTO removed_backup_nodes(node_id) VALUES(?)`, Params: []interface{}{nodeID}},
		{SQL: `UPDATE backup_targets SET enabled=0,generation=generation+1,cleanup_pending=1 WHERE node_id=? AND enabled=1`, Params: []interface{}{nodeID}},
	})
}
