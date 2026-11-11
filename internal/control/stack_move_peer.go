package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/orchestrator"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
)

// ServeStackMovePeer is called only after the lab listener verifies current-member mTLS.
func (s *Server) ServeStackMovePeer(w http.ResponseWriter, r *http.Request, peerID string) {
	if r.Method != http.MethodPost || s.cfg.LabDB == nil ||
		(r.URL.Path != "/internal/stack-moves/prepare" && r.URL.Path != "/internal/stack-moves/reserve" && r.URL.Path != "/internal/stack-moves/activate") {
		http.NotFound(w, r)
		return
	}
	if s.joinPending != nil && s.joinPending() {
		writeError(w, 503, "destination is still joining the lab")
		return
	}
	var move stackMove
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20))
	if err := decoder.Decode(&move); err != nil || decoder.Decode(new(any)) != io.EOF ||
		peerID == "" || move.Stack.PrimaryNodeID != peerID || peerID == move.Destination ||
		move.Destination != s.cfg.LocalNodeID || move.StackID != move.Stack.ID ||
		uuid.Validate(move.StackID) != nil || uuid.Validate(move.OperationID) != nil ||
		move.Generation < 1 || move.Stack.ZFSDataset != "floatlab/"+move.Stack.Name {
		writeError(w, 400, "invalid stack move peer request")
		return
	}
	ctx := r.Context()
	if r.URL.Path == "/internal/stack-moves/prepare" {
		pool, err := s.prepareStackMove(ctx, move)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"network_pool": pool})
		return
	}
	hash, err := move.payloadHash()
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	grant := move.OperationID
	reserving := r.URL.Path == "/internal/stack-moves/reserve"
	if reserving {
		grant = "reserve-" + grant
	}
	rows, err := s.cfg.LabDB.QueryStrong(ctx, rqlite.Statement{
		SQL:    `SELECT 1 FROM stack_move_grants WHERE operation_id=? AND stack_id=? AND payload_hash=?`,
		Params: []any{grant, move.StackID, hash},
	})
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	if len(rows.Values) != 1 {
		writeError(w, 409, "source has not committed this ownership handoff")
		return
	}
	if reserving {
		if err := s.reserveStackMove(ctx, move); err != nil {
			writeError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]string{"status": "reserved"})
		return
	}
	if err := s.acceptStackMove(ctx, move); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"status": "running"})
}

func (s *Server) reserveStackMove(ctx context.Context, move stackMove) error {
	s.moveNames.Lock()
	defer s.moveNames.Unlock()
	if err := s.checkMoveDestination(ctx, move); err != nil {
		return err
	}
	generation, _, err := stackMoveOwner(ctx, s.db, move.StackID)
	if err != nil {
		return err
	}
	if generation >= move.Generation {
		return fmt.Errorf("destination has a newer ownership record")
	}
	if err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `INSERT INTO workload_move_reservations(operation_id,stack_id,name) VALUES(?,?,?) ON CONFLICT(operation_id) DO NOTHING`,
		Params: []any{move.OperationID, move.StackID, move.Stack.Name},
	}}); err != nil {
		return err
	}
	if move.NetworkPool == "" {
		return nil
	}
	pools, err := ipam.ListPools(ctx, s.db)
	if err != nil {
		return err
	}
	pool, err := ipam.SelectPool(pools, move.NetworkPool, "", move.Destination)
	if err != nil {
		return err
	}
	if err := ipam.Eligible(ctx, s.hosts, *pool, move.Destination); err != nil {
		return err
	}
	service, found, err := ipam.StackService(ctx, s.db, move.StackID)
	if err != nil {
		return err
	}
	if found {
		if service.PoolID != pool.ID {
			return fmt.Errorf("destination network reservation belongs to another pool")
		}
		return nil
	}
	_, err = ipam.AllocateIPv4(ctx, s.db, *pool, move.StackID)
	return err
}

func (s *Server) prepareStackMove(ctx context.Context, move stackMove) (string, error) {
	if err := s.checkMoveDestination(ctx, move); err != nil {
		return "", err
	}
	spec, err := compose.ParseLifecycle(move.Stack.ComposeYAML, move.Stack.Name)
	if err != nil {
		return "", err
	}
	pool := ""
	if spec.HasPorts {
		pool, err = s.moveNetworkPool(ctx, move.Destination, move.NetworkPool, spec.NetworkPool)
		if err != nil {
			return "", err
		}
	}
	source, err := movedCompose(move.Stack.ComposeYAML, move.Destination, move.Stack.PrimaryNodeID, pool)
	if err != nil {
		return "", err
	}
	if _, err := compose.ParseLifecycle(source, move.Stack.Name); err != nil {
		return "", err
	}
	return pool, nil
}

func (s *Server) checkMoveDestination(ctx context.Context, move stackMove) error {
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT id FROM stacks WHERE id=? OR name=?`, Params: []any{move.StackID, move.Stack.Name}})
	if err != nil {
		return err
	}
	if len(rows.Values) != 0 {
		return fmt.Errorf("a stack with this ID or name already exists on the destination")
	}
	raw, err := s.hosts.Execute(ctx, move.Destination, "fs.dataset.list", ipc.DatasetListPayload{Parent: "floatlab"})
	if err != nil {
		return err
	}
	var result ipc.DatasetListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	for _, dataset := range result.Datasets {
		if dataset.Name == move.Stack.ZFSDataset || strings.HasPrefix(dataset.Name, move.Stack.ZFSDataset+"/") {
			return fmt.Errorf("workload dataset %s already exists on the destination", move.Stack.ZFSDataset)
		}
	}
	return nil
}

func (s *Server) acceptStackMove(ctx context.Context, move stackMove) error {
	s.moveReceives.Lock()
	defer s.moveReceives.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT phase FROM workload_move_receipts WHERE operation_id=?`, Params: []any{move.OperationID}})
	if err != nil {
		return err
	}
	phase := ""
	if len(rows.Values) != 0 {
		phase = fmt.Sprint(rows.Values[0][0])
	}
	if phase == "complete" {
		return nil
	}
	stack := move.Stack
	stack.PrimaryNodeID = move.Destination
	stack.BackupNodeID = move.Stack.PrimaryNodeID
	stack.UpdatedAt = time.Now().UTC()
	stack.ComposeYAML, err = movedCompose(stack.ComposeYAML, stack.PrimaryNodeID, stack.BackupNodeID, move.NetworkPool)
	if err != nil {
		return err
	}
	spec, err := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
	if err != nil {
		return err
	}
	if phase == "" {
		if err := s.adoptStackMove(ctx, move, &stack); err != nil {
			return err
		}
		phase = "pending"
	}
	setPhase := func(phase string) error {
		return s.db.Execute(ctx, []rqlite.Statement{{
			SQL: `UPDATE workload_move_receipts SET phase=? WHERE operation_id=?`, Params: []any{phase, move.OperationID},
		}})
	}
	if phase == "pending" {
		if err := s.applyMoveState(stack.ID, run.StateIdle, run.StateMoving, run.EventMoveStarted); err != nil {
			return err
		}
		if _, err := s.hosts.Execute(ctx, move.Destination, "fs.move.activate", ipc.StackMoveActivatePayload{
			OperationID: move.OperationID, StackID: move.StackID, SourceNodeID: move.Stack.PrimaryNodeID,
			DatasetPath: stack.ZFSDataset, Snapshot: move.FinalSnapshot, Datasets: move.Datasets,
		}); err != nil {
			return err
		}
		if _, err := s.hosts.Execute(ctx, move.Destination, "compose.source.write", ipc.ComposeSourcePayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: stack.ComposeYAML}); err != nil {
			return err
		}
		if spec.HasPorts {
			if err := s.activateMoveNetwork(ctx, move); err != nil {
				return err
			}
		}
		if err := orchestrator.SyncStackAlerts(ctx, s.db, stack.ID, spec.Alerts); err != nil {
			return err
		}
		if err := setPhase("starting"); err != nil {
			return err
		}
	}
	stackIP := ""
	if spec.HasPorts {
		service, found, err := ipam.StackService(ctx, s.db, move.StackID)
		if err != nil || !found {
			return fmt.Errorf("destination network allocation unavailable: %v", err)
		}
		stackIP, _, _ = strings.Cut(service.Address, "/")
	}
	runtime, err := compose.RuntimeYAML(stack.ComposeYAML, stack.Name, stackIP)
	if err != nil {
		return err
	}
	if _, err := s.hosts.Execute(ctx, move.Destination, "compose.up", ipc.ComposeUpPayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: runtime}); err != nil {
		return err
	}
	completed := map[string]bool{}
	for _, service := range spec.Project.Services {
		for name, dependency := range service.DependsOn {
			if dependency.Condition == types.ServiceConditionCompletedSuccessfully {
				completed[name] = true
			}
		}
	}
	if err := worker.WaitHealthy(ctx, s.hosts, move.Destination, stack.ID, spec.HealthTimeout, completed); err != nil {
		return err
	}
	if err := s.reverseMoveBackups(ctx, move, &stack, spec); err != nil {
		return err
	}
	if err := s.applyMoveState(stack.ID, run.StateMoving, run.StateRunningPrimary, run.EventMoveCompleted); err != nil {
		return err
	}
	return setPhase("complete")
}

func (s *Server) adoptStackMove(ctx context.Context, move stackMove, stack *config.Stack) error {
	s.moveNames.Lock()
	defer s.moveNames.Unlock()
	generation, _, err := stackMoveOwner(ctx, s.db, move.StackID)
	if err != nil {
		return err
	}
	if generation >= move.Generation {
		return fmt.Errorf("destination has a newer ownership record")
	}
	if err := s.checkMoveDestination(ctx, move); err != nil {
		return err
	}
	rows, err := s.db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT 1 FROM workload_move_reservations WHERE operation_id=? AND stack_id=? AND name=?`,
		Params: []any{move.OperationID, move.StackID, stack.Name},
	})
	if err != nil {
		return err
	}
	if len(rows.Values) != 1 {
		return fmt.Errorf("destination move reservation is missing")
	}
	return s.db.Execute(ctx, []rqlite.Statement{
		config.StackInsert(stack),
		move.ownerStatement(),
		{SQL: `INSERT INTO workload_move_receipts(operation_id,stack_id,phase) VALUES(?,?,'pending')`, Params: []any{move.OperationID, move.StackID}},
		{SQL: `DELETE FROM workload_move_reservations WHERE operation_id=?`, Params: []any{move.OperationID}},
	})
}

func (s *Server) activateMoveNetwork(ctx context.Context, move stackMove) error {
	service, found, err := ipam.StackService(ctx, s.db, move.StackID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("destination address reservation is missing")
	}
	if _, err := s.hosts.Execute(ctx, move.Destination, "net.service.add", service); err != nil {
		return err
	}
	address, _, _ := strings.Cut(service.Address, "/")
	if err := s.db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO stack_runtime(stack_id,stack_ip,network_pool,active_node_id,updated_at) VALUES(?,?,?,?,?)
			ON CONFLICT(stack_id) DO UPDATE SET stack_ip=excluded.stack_ip,network_pool=excluded.network_pool,
			active_node_id=excluded.active_node_id,updated_at=excluded.updated_at,deleted_at=NULL`,
		Params: []any{move.StackID, address, service.PoolID, move.Destination, time.Now().UTC()},
	}}); err != nil {
		return err
	}
	return ipam.ActivateIPv4(ctx, s.db, move.StackID)
}

func (s *Server) reverseMoveBackups(ctx context.Context, move stackMove, stack *config.Stack, spec *compose.LifecycleSpec) error {
	if err := s.store.SyncBackupTargets(ctx, stack); err != nil {
		return err
	}
	for _, mount := range spec.Mounts {
		policy := "h/24 d/28"
		origin := "api"
		for _, target := range move.Targets {
			if !target.Enabled || target.Dataset != mount.Dataset {
				continue
			}
			if target.NodeID == move.Destination {
				policy = target.Schedule
				origin = target.Origin
				continue
			}
			target.SourceNodeID = move.Destination
			if err := s.store.PutBackupTarget(ctx, target); err != nil {
				return err
			}
		}
		if err := s.store.PutBackupTarget(ctx, config.BackupTarget{
			StackID: move.StackID, Dataset: mount.Dataset, NodeID: stack.BackupNodeID, SourceNodeID: stack.PrimaryNodeID,
			Schedule: policy, LocalPolicy: config.MountPolicy(mount), Origin: origin,
		}); err != nil {
			return err
		}
	}
	return nil
}
