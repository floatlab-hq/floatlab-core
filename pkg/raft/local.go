package raft

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	hraft "github.com/hashicorp/raft"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

type localState struct {
	mu     sync.Mutex
	db     *rqlite.Client
	nodeID string
}

// NewLocalNode preserves lifecycle subscribers while committing authority locally.
func NewLocalNode(ctx context.Context, db *rqlite.Client, nodeID string, log *zap.Logger) (*Node, error) {
	if db.LocalPath() == "" || nodeID == "" {
		return nil, fmt.Errorf("local lifecycle requires a local database and node identity")
	}
	id, err := db.LocalIdentity(ctx)
	if err != nil {
		return nil, err
	}
	if id.NodeID != nodeID {
		return nil, fmt.Errorf("lifecycle database belongs to node %s", id.NodeID)
	}
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `CREATE TABLE IF NOT EXISTS workload_lifecycle (stack_id TEXT PRIMARY KEY, entry TEXT NOT NULL)`}}); err != nil {
		return nil, err
	}
	n := &Node{fsm: NewFSM(), log: log, local: &localState{db: db, nodeID: nodeID}}
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT entry FROM workload_lifecycle ORDER BY stack_id`})
	if err != nil {
		return nil, err
	}
	for _, row := range rows.Values {
		if len(row) != 1 {
			return nil, fmt.Errorf("invalid persisted lifecycle row")
		}
		entry, ok := row[0].(string)
		if !ok {
			return nil, fmt.Errorf("invalid persisted lifecycle entry")
		}
		var persisted run.StackStateChanged
		if err := json.Unmarshal([]byte(entry), &persisted); err != nil {
			return nil, err
		}
		if persisted.NodeID != "" && persisted.NodeID != nodeID {
			return nil, fmt.Errorf("persisted lifecycle belongs to another node")
		}
		if err, ok := n.fsm.Apply(&hraft.Log{Data: []byte(entry)}).(error); ok {
			return nil, err
		}
	}
	return n, nil
}

func (n *Node) applyLocal(entry run.StackStateChanged, timeout time.Duration) error {
	n.local.mu.Lock()
	defer n.local.mu.Unlock()
	if entry.NodeID != "" && entry.NodeID != n.local.nodeID {
		return fmt.Errorf("workload lifecycle belongs to another node")
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := n.local.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO workload_lifecycle(stack_id,entry) VALUES(?,?) ON CONFLICT(stack_id) DO UPDATE SET entry=excluded.entry`, Params: []any{entry.StackID, string(b)}}}); err != nil {
		return err
	}
	if err, ok := n.fsm.Apply(&hraft.Log{Data: b}).(error); ok {
		return err
	}
	return nil
}
