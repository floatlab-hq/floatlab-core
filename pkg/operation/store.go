package operation

import (
	"context"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type Operation struct {
	ID         string    `json:"operation_id"`
	StackID    string    `json:"stack_id,omitempty"`
	Action     string    `json:"action"`
	Actor      string    `json:"actor"`
	State      string    `json:"state"`
	Checkpoint string    `json:"checkpoint"`
	Payload    string    `json:"-"`
	Error      string    `json:"error,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Store struct{ db *rqlite.Client }

func NewStore(db *rqlite.Client) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, op Operation) error {
	return s.db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO operations(id, stack_id, action, actor, state, checkpoint, payload, created_at, updated_at)
		      VALUES(?,?,?,?,?,?,?,?,?)`,
		Params: []interface{}{op.ID, op.StackID, op.Action, op.Actor, op.State, op.Checkpoint, op.Payload, op.CreatedAt, op.UpdatedAt},
	}})
}

func (s *Store) Get(ctx context.Context, id string) (*Operation, error) {
	result, err := s.db.Query(ctx, rqlite.Statement{
		SQL: `SELECT id, stack_id, action, actor, state, checkpoint, payload, error, created_at, updated_at
		      FROM operations WHERE id=?`,
		Params: []interface{}{id},
	})
	if err != nil {
		return nil, err
	}
	if len(result.Values) == 0 {
		return nil, fmt.Errorf("operation %s not found", id)
	}
	return scan(result.Values[0]), nil
}

func (s *Store) Update(ctx context.Context, id, state, checkpoint, errMsg string) error {
	return s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE operations SET state=?, checkpoint=?, error=NULLIF(?, ''), updated_at=? WHERE id=?`,
		Params: []interface{}{state, checkpoint, errMsg, time.Now().UTC(), id},
	}})
}

func (s *Store) FinishForStack(ctx context.Context, stackID, action, state, errMsg string) error {
	return s.db.Execute(ctx, []rqlite.Statement{{
		SQL: `UPDATE operations SET state=?, checkpoint=?, error=NULLIF(?, ''), updated_at=?
		      WHERE id=(SELECT id FROM operations WHERE stack_id=? AND action=? AND state IN ('pending','running') ORDER BY created_at LIMIT 1)`,
		Params: []interface{}{state, state, errMsg, time.Now().UTC(), stackID, action},
	}})
}

func (s *Store) Active(ctx context.Context) ([]*Operation, error) {
	result, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT id, stack_id, action, actor, state, checkpoint, payload, error, created_at, updated_at FROM operations WHERE state IN ('pending','running') ORDER BY created_at`})
	if err != nil {
		return nil, err
	}
	operations := make([]*Operation, 0, len(result.Values))
	for _, row := range result.Values {
		operations = append(operations, scan(row))
	}
	return operations, nil
}

func (s *Store) ActiveForStack(ctx context.Context, stackID string) (*Operation, error) {
	result, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT id, stack_id, action, actor, state, checkpoint, payload, error, created_at, updated_at FROM operations WHERE stack_id=? AND state IN ('pending','running') ORDER BY created_at LIMIT 1`, Params: []interface{}{stackID}})
	if err != nil || len(result.Values) == 0 {
		return nil, err
	}
	return scan(result.Values[0]), nil
}

func scan(row []interface{}) *Operation {
	op := &Operation{}
	if len(row) < 10 {
		return op
	}
	op.ID, _ = row[0].(string)
	op.StackID, _ = row[1].(string)
	op.Action, _ = row[2].(string)
	op.Actor, _ = row[3].(string)
	op.State, _ = row[4].(string)
	op.Checkpoint, _ = row[5].(string)
	op.Payload, _ = row[6].(string)
	op.Error, _ = row[7].(string)
	if v, ok := row[8].(string); ok {
		op.CreatedAt, _ = time.Parse(time.RFC3339, v)
	}
	if v, ok := row[9].(string); ok {
		op.UpdatedAt, _ = time.Parse(time.RFC3339, v)
	}
	return op
}
