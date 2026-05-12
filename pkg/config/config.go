package config

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Store provides CRUD operations for all FloatLab configuration entities,
// backed by rqlite. It also maintains a change watch channel for reactive updates.
type Store struct {
	db      *rqlite.Client
	mu      sync.RWMutex
	watches []chan ChangeEvent
}

func NewStore(db *rqlite.Client) *Store {
	return &Store{db: db}
}

// Watch returns a channel that receives a ChangeEvent whenever any entity is
// created, updated, or deleted. The caller must drain the channel.
func (s *Store) Watch() <-chan ChangeEvent {
	ch := make(chan ChangeEvent, 64)
	s.mu.Lock()
	s.watches = append(s.watches, ch)
	s.mu.Unlock()
	return ch
}

func (s *Store) notify(ev ChangeEvent) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ch := range s.watches {
		select {
		case ch <- ev:
		default:
		}
	}
}

// ---- Nodes ----

func (s *Store) CreateNode(ctx context.Context, n *Node) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	n.CreatedAt = time.Now().UTC()
	n.UpdatedAt = n.CreatedAt
	addrsJSON, _ := json.Marshal(n.Addresses)
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT INTO nodes(id, cluster_uuid, name, addresses, created_at, updated_at) VALUES(?,?,?,?,?,?)`,
		Params: []interface{}{n.ID, n.ClusterUUID, n.Name, string(addrsJSON), n.CreatedAt, n.UpdatedAt},
	}})
	if err != nil {
		return fmt.Errorf("config: create node: %w", err)
	}
	s.notify(ChangeEvent{Entity: "node", Action: "create", ID: n.ID})
	return nil
}

func (s *Store) GetNode(ctx context.Context, id string) (*Node, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT id, cluster_uuid, name, addresses, created_at, updated_at FROM nodes WHERE id=?`,
		Params: []interface{}{id},
	})
	if err != nil {
		return nil, fmt.Errorf("config: get node: %w", err)
	}
	if len(res.Values) == 0 {
		return nil, fmt.Errorf("config: node %s not found", id)
	}
	return scanNode(res.Values[0])
}

func (s *Store) ListNodes(ctx context.Context) ([]*Node, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id, cluster_uuid, name, addresses, created_at, updated_at FROM nodes ORDER BY name`,
	})
	if err != nil {
		return nil, fmt.Errorf("config: list nodes: %w", err)
	}
	nodes := make([]*Node, 0, len(res.Values))
	for _, row := range res.Values {
		n, err := scanNode(row)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func (s *Store) UpdateNode(ctx context.Context, n *Node) error {
	n.UpdatedAt = time.Now().UTC()
	addrsJSON, _ := json.Marshal(n.Addresses)
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE nodes SET cluster_uuid=?, name=?, addresses=?, updated_at=? WHERE id=?`,
		Params: []interface{}{n.ClusterUUID, n.Name, string(addrsJSON), n.UpdatedAt, n.ID},
	}})
	if err != nil {
		return fmt.Errorf("config: update node: %w", err)
	}
	s.notify(ChangeEvent{Entity: "node", Action: "update", ID: n.ID})
	return nil
}

func (s *Store) DeleteNode(ctx context.Context, id string) error {
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM nodes WHERE id=?`,
		Params: []interface{}{id},
	}})
	if err != nil {
		return fmt.Errorf("config: delete node: %w", err)
	}
	s.notify(ChangeEvent{Entity: "node", Action: "delete", ID: id})
	return nil
}

// ---- Stacks ----

func (s *Store) CreateStack(ctx context.Context, stack *Stack) error {
	if stack.ID == "" {
		stack.ID = uuid.New().String()
	}
	stack.CreatedAt = time.Now().UTC()
	stack.UpdatedAt = stack.CreatedAt
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO stacks(id, name, icon, primary_node_id, backup_node_id, compose_yaml,
		      zfs_dataset, snapshot_schedule, replication_schedule, backup_schedule, backup_target,
		      failover_mode, auto_trigger_after, created_at, updated_at)
		      VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		Params: []interface{}{
			stack.ID, stack.Name, stack.Icon, stack.PrimaryNodeID, stack.BackupNodeID,
			stack.ComposeYAML, stack.ZFSDataset, stack.SnapshotSchedule,
			stack.ReplicationSchedule, stack.BackupSchedule, stack.BackupTarget,
			stack.FailoverMode, stack.AutoTriggerAfter,
			stack.CreatedAt, stack.UpdatedAt,
		},
	}})
	if err != nil {
		return fmt.Errorf("config: create stack: %w", err)
	}
	s.notify(ChangeEvent{Entity: "stack", Action: "create", ID: stack.ID})
	return nil
}

func (s *Store) GetStack(ctx context.Context, id string) (*Stack, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id, name, icon, primary_node_id, backup_node_id, compose_yaml,
		      zfs_dataset, snapshot_schedule, replication_schedule, backup_schedule, backup_target,
		      failover_mode, auto_trigger_after, created_at, updated_at FROM stacks WHERE id=?`,
		Params: []interface{}{id},
	})
	if err != nil {
		return nil, fmt.Errorf("config: get stack: %w", err)
	}
	if len(res.Values) == 0 {
		return nil, fmt.Errorf("config: stack %s not found", id)
	}
	return scanStack(res.Values[0])
}

func (s *Store) ListStacks(ctx context.Context) ([]*Stack, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id, name, icon, primary_node_id, backup_node_id, compose_yaml,
		      zfs_dataset, snapshot_schedule, replication_schedule, backup_schedule, backup_target,
		      failover_mode, auto_trigger_after, created_at, updated_at FROM stacks ORDER BY name`,
	})
	if err != nil {
		return nil, fmt.Errorf("config: list stacks: %w", err)
	}
	stacks := make([]*Stack, 0, len(res.Values))
	for _, row := range res.Values {
		st, err := scanStack(row)
		if err != nil {
			return nil, err
		}
		stacks = append(stacks, st)
	}
	return stacks, nil
}

func (s *Store) UpdateStackCompose(ctx context.Context, id, composeYAML string) error {
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE stacks SET compose_yaml=?, updated_at=? WHERE id=?`,
		Params: []interface{}{composeYAML, time.Now().UTC(), id},
	}})
	if err != nil {
		return fmt.Errorf("config: update stack compose: %w", err)
	}
	s.notify(ChangeEvent{Entity: "stack", Action: "update", ID: id})
	return nil
}

func (s *Store) DeleteStack(ctx context.Context, id string) error {
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM stacks WHERE id=?`,
		Params: []interface{}{id},
	}})
	if err != nil {
		return fmt.Errorf("config: delete stack: %w", err)
	}
	s.notify(ChangeEvent{Entity: "stack", Action: "delete", ID: id})
	return nil
}

// ---- Scanner helpers ----

func scanNode(row []interface{}) (*Node, error) {
	n := &Node{}
	if len(row) < 6 {
		return nil, fmt.Errorf("config: scanNode: short row")
	}
	n.ID, _ = row[0].(string)
	n.ClusterUUID, _ = row[1].(string)
	n.Name, _ = row[2].(string)
	if addrsStr, ok := row[3].(string); ok {
		_ = json.Unmarshal([]byte(addrsStr), &n.Addresses)
	}
	if t, ok := row[4].(string); ok {
		n.CreatedAt, _ = time.Parse(time.RFC3339, t)
	}
	if t, ok := row[5].(string); ok {
		n.UpdatedAt, _ = time.Parse(time.RFC3339, t)
	}
	return n, nil
}

func scanStack(row []interface{}) (*Stack, error) {
	if len(row) < 15 {
		return nil, fmt.Errorf("config: scanStack: short row")
	}
	st := &Stack{}
	st.ID, _ = row[0].(string)
	st.Name, _ = row[1].(string)
	st.Icon, _ = row[2].(string)
	st.PrimaryNodeID, _ = row[3].(string)
	st.BackupNodeID, _ = row[4].(string)
	st.ComposeYAML, _ = row[5].(string)
	st.ZFSDataset, _ = row[6].(string)
	st.SnapshotSchedule, _ = row[7].(string)
	st.ReplicationSchedule, _ = row[8].(string)
	st.BackupSchedule, _ = row[9].(string)
	st.BackupTarget, _ = row[10].(string)
	st.FailoverMode, _ = row[11].(string)
	st.AutoTriggerAfter, _ = row[12].(string)
	if t, ok := row[13].(string); ok {
		st.CreatedAt, _ = time.Parse(time.RFC3339, t)
	}
	if t, ok := row[14].(string); ok {
		st.UpdatedAt, _ = time.Parse(time.RFC3339, t)
	}
	return st, nil
}

// ---- Networks ----

func (s *Store) CreateNetwork(ctx context.Context, n *Network) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT INTO networks(id, name, prefix, reserved_min, reserved_max) VALUES(?,?,?,?,?)`,
		Params: []interface{}{n.ID, n.Name, n.Prefix, n.ReservedMin, n.ReservedMax},
	}})
	if err != nil {
		return fmt.Errorf("config: create network: %w", err)
	}
	s.notify(ChangeEvent{Entity: "network", Action: "create", ID: n.ID})
	return nil
}

func (s *Store) GetNetwork(ctx context.Context, id string) (*Network, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT id, name, prefix, reserved_min, reserved_max FROM networks WHERE id=?`,
		Params: []interface{}{id},
	})
	if err != nil {
		return nil, fmt.Errorf("config: get network: %w", err)
	}
	if len(res.Values) == 0 {
		return nil, fmt.Errorf("config: network %s not found", id)
	}
	return scanNetwork(res.Values[0])
}

func (s *Store) ListNetworks(ctx context.Context) ([]*Network, error) {
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id, name, prefix, reserved_min, reserved_max FROM networks ORDER BY name`,
	})
	if err != nil {
		return nil, fmt.Errorf("config: list networks: %w", err)
	}
	nets := make([]*Network, 0, len(res.Values))
	for _, row := range res.Values {
		n, err := scanNetwork(row)
		if err != nil {
			return nil, err
		}
		nets = append(nets, n)
	}
	return nets, nil
}

func (s *Store) DeleteNetwork(ctx context.Context, id string) error {
	err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM networks WHERE id=?`,
		Params: []interface{}{id},
	}})
	if err != nil {
		return fmt.Errorf("config: delete network: %w", err)
	}
	s.notify(ChangeEvent{Entity: "network", Action: "delete", ID: id})
	return nil
}

func scanNetwork(row []interface{}) (*Network, error) {
	if len(row) < 5 {
		return nil, fmt.Errorf("config: scanNetwork: short row")
	}
	n := &Network{}
	n.ID, _ = row[0].(string)
	n.Name, _ = row[1].(string)
	n.Prefix, _ = row[2].(string)
	n.ReservedMin, _ = row[3].(string)
	n.ReservedMax, _ = row[4].(string)
	return n, nil
}
