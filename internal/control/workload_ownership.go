package control

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

var errStackOwnerClaimConflict = errors.New("stack ownership changed during the handoff")

func (s *Server) cachedSharedMoveOwners(ctx context.Context, id string) map[string]moveOwnerRecord {
	if s.cfg.LabDB == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	query := rqlite.Statement{SQL: `SELECT stack_id,node_id,generation FROM workload_move_owners`}
	if id != "" {
		query.SQL += " WHERE stack_id=?"
		query.Params = []any{id}
	}
	rows, err := s.cfg.LabDB.QueryLocal(ctx, query)
	if err != nil {
		return nil
	}
	owners := make(map[string]moveOwnerRecord, len(rows.Values))
	for _, row := range rows.Values {
		owners[fmt.Sprint(row[0])] = moveOwnerRecord{node: fmt.Sprint(row[1]), generation: int64(number(row[2]))}
	}
	return owners
}

// IsLocalWorkloadOwner fences background tasks after the lab records a newer owner.
func (s *Server) IsLocalWorkloadOwner(ctx context.Context, id string) (bool, error) {
	generation, owner, err := stackMoveOwner(ctx, s.db, id)
	if err != nil {
		return false, err
	}
	if shared := s.cachedSharedMoveOwners(ctx, id)[id]; shared.generation > generation || (shared.node != "" && shared.generation == generation) {
		owner = shared.node
	}
	return owner == "" || owner == s.cfg.LocalNodeID, nil
}

func sharedStackOwner(ctx context.Context, db *rqlite.Client, id string) (moveOwnerRecord, error) {
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT node_id,generation FROM workload_move_owners WHERE stack_id=?`, Params: []any{id}})
	if err != nil || len(rows.Values) == 0 {
		return moveOwnerRecord{}, err
	}
	row := rows.Values[0]
	return moveOwnerRecord{node: fmt.Sprint(row[0]), generation: int64(number(row[1]))}, nil
}

func claimStackOwner(id, source, destination, operation string, generation int64) rqlite.Statement {
	return rqlite.Statement{
		SQL: `INSERT INTO workload_move_owners(stack_id,node_id,generation,operation_id) VALUES(?,?,?,?)
			ON CONFLICT(stack_id) DO UPDATE SET node_id=excluded.node_id,generation=excluded.generation,operation_id=excluded.operation_id
			WHERE workload_move_owners.generation=excluded.generation-1 AND workload_move_owners.node_id=?`,
		Params: []any{id, destination, generation, operation, source},
	}
}

func verifyStackOwnerClaim(ctx context.Context, db *rqlite.Client, id, destination, operation string, generation int64) error {
	rows, err := db.QueryStrong(ctx, rqlite.Statement{
		SQL:    `SELECT 1 FROM workload_move_owners WHERE stack_id=? AND node_id=? AND generation=? AND operation_id=?`,
		Params: []any{id, destination, generation, operation},
	})
	if err != nil {
		return err
	}
	if len(rows.Values) != 1 {
		return errStackOwnerClaimConflict
	}
	return nil
}

// CommitReplicaTakeover adopts a staged recovery only after its shared ownership claim commits.
func (s *Server) CommitReplicaTakeover(ctx context.Context, p worker.BackupRestorePayload) error {
	stack := p.TakeoverStack
	if s.cfg.LabDB == nil || p.OwnerGeneration < 1 || p.DestinationNodeID != s.cfg.LocalNodeID || stack == nil || stack.ID != p.StackID || stack.PrimaryNodeID != p.DestinationNodeID {
		return fmt.Errorf("invalid replica ownership claim")
	}
	s.moveNames.Lock()
	defer s.moveNames.Unlock()
	reservation, err := s.db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT 1 FROM workload_move_reservations WHERE operation_id=? AND stack_id=? AND name=?`,
		Params: []any{p.OperationID, stack.ID, stack.Name},
	})
	if err != nil {
		return errors.Join(worker.ErrMoveMustResume, err)
	}
	if len(reservation.Values) == 0 {
		if err := verifyStackOwnerClaim(ctx, s.db, p.StackID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration); err != nil {
			return err
		}
		return verifyStackOwnerClaim(ctx, s.cfg.LabDB, p.StackID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration)
	}
	move := stackMove{Stack: *stack, StackID: p.StackID, Destination: p.DestinationNodeID, OperationID: p.OperationID, Generation: p.OwnerGeneration}
	err = verifyStackOwnerClaim(ctx, s.cfg.LabDB, p.StackID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration)
	if errors.Is(err, errStackOwnerClaimConflict) {
		if err := s.checkMoveDestination(ctx, move); err != nil {
			return err
		}
		claim := claimStackOwner(p.StackID, p.SourceNodeID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration)
		if err := s.cfg.LabDB.Execute(ctx, []rqlite.Statement{claim}); err != nil {
			return errors.Join(worker.ErrMoveMustResume, err)
		}
		err = verifyStackOwnerClaim(ctx, s.cfg.LabDB, p.StackID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration)
	}
	if err != nil {
		if !errors.Is(err, errStackOwnerClaimConflict) {
			return errors.Join(worker.ErrMoveMustResume, err)
		}
		return err
	}
	statements := []rqlite.Statement{
		config.StackInsert(stack),
		move.ownerStatement(),
		{SQL: `DELETE FROM workload_move_reservations WHERE operation_id=?`, Params: []any{p.OperationID}},
	}
	if err := s.db.Execute(ctx, appendFailedWorkloadUpgrades(statements, stack.ID, p.FailedUpgrades)); err != nil {
		return errors.Join(worker.ErrMoveMustResume, err)
	}
	return verifyStackOwnerClaim(ctx, s.db, p.StackID, p.DestinationNodeID, p.OperationID, p.OwnerGeneration)
}
