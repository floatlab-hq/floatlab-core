package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func (s *Server) RunStackMove(ctx context.Context, raw json.RawMessage) error {
	var move stackMove
	if err := json.Unmarshal(raw, &move); err != nil {
		return err
	}
	op, err := s.ops.Get(ctx, move.OperationID)
	if err != nil || op.State == "succeeded" {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	phase := op.Checkpoint
	save := func(next string) error {
		raw, err := json.Marshal(move)
		if err != nil {
			return err
		}
		err = s.db.Execute(ctx, []rqlite.Statement{
			{SQL: `UPDATE tasks SET payload=? WHERE id=?`, Params: []any{string(raw), "move-" + move.OperationID}},
			{SQL: `UPDATE operations SET state='running', checkpoint=?, error=NULL, updated_at=? WHERE id=?`, Params: []any{next, time.Now().UTC(), move.OperationID}},
		})
		if err == nil {
			phase = next
		}
		return err
	}
	err = s.advanceStackMove(ctx, &move, &phase, save)
	if err != nil {
		status, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.ops.Update(status, move.OperationID, "running", phase, err.Error())
		if phase != "seeding" && phase != "accepted" && phase != "running" {
			// The destination may already have reserved resources or accepted ownership.
			return errors.Join(worker.ErrMoveMustResume, err)
		}
	}
	return err
}

func (s *Server) advanceStackMove(ctx context.Context, move *stackMove, phase *string, save func(string) error) error {
	source := move.Stack.PrimaryNodeID
	if source != s.cfg.LocalNodeID || s.peerWorkloads == nil || s.cfg.LabDB == nil {
		return errors.New("move task requires its source lab member")
	}
	if *phase == "accepted" || *phase == "running" {
		if err := save("seeding"); err != nil {
			return err
		}
	}
	if *phase == "seeding" {
		if move.SeedSnapshot == "" {
			var err error
			move.SeedSnapshot, err = store.AutomatedSnapshotName(time.Now().UTC())
			if err != nil {
				return err
			}
			if err := save("seeding"); err != nil {
				return err
			}
		}
		if err := s.replicateStackMove(ctx, *move, move.SeedSnapshot); err != nil {
			return err
		}
		if _, err := s.cfg.LabDB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT operation_id FROM stack_move_grants WHERE operation_id=?`, Params: []any{move.OperationID}}); err != nil {
			return fmt.Errorf("lab quorum required for cutover: %w", err)
		}
		instance, ok := s.raft.FSM().State(move.StackID)
		if !ok || instance.State != run.StateRunningPrimary {
			return errors.New("stack is no longer running on the source")
		}
		if err := save("reserving-destination"); err != nil {
			return err
		}
	}
	if *phase == "reserving-destination" {
		if err := s.grantStackMove(ctx, "reserve-"+move.OperationID, *move); err != nil {
			return err
		}
		var result struct{ Status string }
		if err := s.backupPeerRequest(ctx, move.Destination, http.MethodPost, "/internal/stack-moves/reserve", move, &result); err != nil {
			return err
		}
		if result.Status != "reserved" {
			return errors.New("destination has not reserved workload resources")
		}
		if err := save("stopping-source"); err != nil {
			return err
		}
	}
	if *phase == "stopping-source" {
		if instance, ok := s.raft.FSM().State(move.StackID); !ok || instance.State != run.StateMoving {
			if err := s.applyMoveState(move.StackID, run.StateRunningPrimary, run.StateMoving, run.EventMoveStarted); err != nil {
				return err
			}
		}
		if _, err := s.hosts.Execute(ctx, source, "compose.down", ipc.ComposeDownPayload{StackID: move.StackID, DatasetPath: move.Stack.ZFSDataset}); err != nil {
			return err
		}
		if err := s.waitMoveSourceStopped(ctx, *move); err != nil {
			return err
		}
		// Pick a fresh timestamp bucket after the source has stopped.
		now := time.Now().UTC()
		next := now.Truncate(3 * time.Second).Add(3 * time.Second)
		timer := time.NewTimer(time.Until(next))
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		var err error
		move.FinalSnapshot, err = store.AutomatedSnapshotName(time.Now().UTC())
		if err != nil {
			return err
		}
		if err := save("final-sync"); err != nil {
			return err
		}
	}
	if *phase == "final-sync" {
		if err := s.replicateStackMove(ctx, *move, move.FinalSnapshot); err != nil {
			return err
		}
		if err := save("transferring-ownership"); err != nil {
			return err
		}
	}
	if *phase == "transferring-ownership" {
		if _, err := s.hosts.Execute(ctx, source, "fs.move.archive", ipc.StackMoveArchivePayload{
			OperationID: move.OperationID, DatasetPath: move.Stack.ZFSDataset, Snapshot: move.FinalSnapshot, Datasets: move.Datasets,
		}); err != nil {
			return err
		}
		service, exists, err := ipam.StackService(ctx, s.db, move.StackID)
		if err != nil {
			return err
		}
		if exists {
			if _, err := s.hosts.Execute(ctx, source, "net.service.del", service); err != nil {
				return err
			}
		}
		if err := s.db.Execute(ctx, []rqlite.Statement{
			move.ownerStatement(),
			{SQL: `DELETE FROM stacks WHERE id=?`, Params: []any{move.StackID}},
			{SQL: `DELETE FROM backup_targets WHERE stack_id=?`, Params: []any{move.StackID}},
			{SQL: `DELETE FROM network_allocations WHERE stack_id=?`, Params: []any{move.StackID}},
			{SQL: `DELETE FROM stack_runtime WHERE stack_id=?`, Params: []any{move.StackID}},
			{SQL: `UPDATE stack_alert_rules SET active=0 WHERE stack_id=?`, Params: []any{move.StackID}},
			{SQL: `UPDATE tasks SET state='done' WHERE stack_id=? AND state='pending' AND type IN (?,?)`, Params: []any{move.StackID, worker.TaskSnapshotCreate, worker.TaskReplTrigger}},
		}); err != nil {
			return err
		}
		// The grant can only be issued after relinquishing the local stack.
		if err := s.grantStackMove(ctx, move.OperationID, *move); err != nil {
			return err
		}
		if err := save("starting-destination"); err != nil {
			return err
		}
	}
	if *phase != "starting-destination" {
		return fmt.Errorf("unexpected stack move checkpoint %q", *phase)
	}
	var result struct{ Status string }
	if err := s.backupPeerRequest(ctx, move.Destination, http.MethodPost, "/internal/stack-moves/activate", move, &result); err != nil {
		return err
	}
	if result.Status != "running" {
		return errors.New("destination has not confirmed that the stack is running")
	}
	if err := operation.RecordEvent(ctx, s.db, operation.Event{StackID: move.StackID, Type: "Move", Outcome: "succeeded", OperationID: move.OperationID}); err != nil {
		return err
	}
	return s.ops.Update(ctx, move.OperationID, "succeeded", "succeeded", "")
}

func (s *Server) grantStackMove(ctx context.Context, id string, move stackMove) error {
	hash, err := move.payloadHash()
	if err != nil {
		return err
	}
	return s.cfg.LabDB.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT INTO stack_move_grants(operation_id,stack_id,payload_hash) VALUES(?,?,?) ON CONFLICT(operation_id) DO NOTHING`,
		Params: []any{id, move.StackID, hash},
	}})
}

func (s *Server) replicateStackMove(ctx context.Context, move stackMove, snapshot string) error {
	origin, ok := s.peerWorkloads.lookup(move.Destination)
	if !ok {
		return errors.New("move destination is no longer an active lab member")
	}
	peer, err := url.Parse(origin)
	if err != nil || peer.Hostname() == "" {
		return errors.New("move destination has no peer address")
	}
	for _, dataset := range move.Datasets {
		generation := int64(1)
		for _, target := range move.Targets {
			if target.Dataset == dataset.Dataset && target.NodeID == move.Destination {
				generation = target.Generation
				if !target.Enabled {
					generation++
				}
				break
			}
		}
		source := move.Stack.PrimaryNodeID
		if _, err := s.hosts.Execute(ctx, source, "fs.snapshot.create", ipc.SnapshotCreatePayload{
			Dataset: dataset.Dataset, Name: snapshot,
			Properties: map[string]string{"floatlab:operation-id": "snapshot:" + dataset.Dataset + "@" + snapshot, "floatlab:purpose": "move"},
		}); err != nil {
			return err
		}
		if _, err := s.hosts.Execute(ctx, source, "fs.repl.send", ipc.ReplSendPayload{
			Dataset: dataset.Dataset, Snapshot: snapshot, SourceNodeID: source, DestNodeID: move.Destination,
			DestHost: peer.Hostname(), DestDataset: store.MoveBackupDataset(source, move.Stack.ZFSDataset, move.StackID, dataset.Dataset),
			Generation: generation, JobID: move.OperationID,
		}); err != nil {
			return fmt.Errorf("replicate %s: %w", dataset.Dataset, err)
		}
	}
	return nil
}

func (s *Server) waitMoveSourceStopped(ctx context.Context, move stackMove) error {
	raw, err := s.hosts.Execute(ctx, move.Stack.PrimaryNodeID, "docker.list", ipc.DockerListPayload{StackID: move.StackID})
	if err != nil {
		return err
	}
	var result ipc.DockerListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, container := range result.Containers {
		if container.State != "exited" && container.State != "dead" && container.State != "created" {
			return fmt.Errorf("source container %s is still %s", container.Name, container.State)
		}
	}
	return nil
}

func (move stackMove) payloadHash() (string, error) {
	raw, err := json.Marshal(move)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw)), nil
}

func (move stackMove) ownerStatement() rqlite.Statement {
	return rqlite.Statement{
		SQL: `INSERT INTO workload_move_owners(stack_id,node_id,generation) VALUES(?,?,?)
			ON CONFLICT(stack_id) DO UPDATE SET node_id=excluded.node_id,generation=excluded.generation
			WHERE workload_move_owners.generation < excluded.generation`,
		Params: []any{move.StackID, move.Destination, move.Generation},
	}
}
