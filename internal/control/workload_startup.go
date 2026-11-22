package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const workloadStartupMigration = "managed-workload-startup-v1"
const workloadStartupPath = "/internal/workload-startup/"

type workloadStartup struct {
	enabled bool
	peers   func() []string
	ready   atomic.Bool
	pending sync.Map
}

type startupCandidate struct {
	checked  bool
	resuming bool
}

type workloadStartupStatus struct {
	Running    bool   `json:"running"`
	Owner      string `json:"owner_node_id,omitempty"`
	Generation int64  `json:"generation,omitempty"`
}

func (s *Server) SetWorkloadStartupPeers(peers func() []string) {
	s.startup.peers = peers
}

// EnableWorkloadStartup publishes proof that this host leaves boot activation
// to the control plane. A recovery target can verify it from the source replica.
func (s *Server) EnableWorkloadStartup(ctx context.Context) error {
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := s.hosts.Execute(probe, s.cfg.LocalNodeID, "docker.startup.guard", nil)
	if err != nil {
		var rpc *ipc.RPCError
		if !errors.As(err, &rpc) || rpc.Code != "cmd.unknown" {
			return fmt.Errorf("verify Docker startup guard: %w", err)
		}
	} else if err = json.Unmarshal(raw, &s.startup.enabled); err != nil {
		return fmt.Errorf("read Docker startup guard: %w", err)
	}
	if !s.startup.enabled {
		return s.db.Execute(ctx, []rqlite.Statement{{SQL: `DELETE FROM local_migrations WHERE name=?`, Params: []any{workloadStartupMigration}}})
	}
	return s.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT OR IGNORE INTO local_migrations(name,applied_at) VALUES(?,?)`, Params: []any{workloadStartupMigration, time.Now().UTC()}}})
}

// CheckWorkloadStart also guards durable work queued before a former owner lost power.
func (s *Server) CheckWorkloadStart(ctx context.Context, stackID string) error {
	if !s.startup.enabled {
		return nil
	}
	if !s.startup.ready.Load() {
		return errors.New("waiting for workload startup checks")
	}
	if candidate, waiting := s.startup.pending.Load(stackID); waiting && !candidate.(startupCandidate).checked {
		return errors.New("workload startup checks are incomplete or another copy is responding")
	}
	local, err := s.IsLocalWorkloadOwner(ctx, stackID)
	if err == nil && !local {
		return errors.New("workload is owned by another lab node")
	}
	return err
}

func (s *Server) RestoreWorkloadsOnStartup(ctx context.Context) error {
	if !s.startup.enabled {
		return nil
	}
	if !s.startup.ready.Load() {
		if s.startup.peers != nil && len(s.startup.peers()) != 0 {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	stacks, err := s.store.ListStacks(ctx)
	if err != nil {
		return err
	}
	local := make(map[string]*config.Stack, len(stacks))
	for _, stack := range stacks {
		if stack.PrimaryNodeID == s.cfg.LocalNodeID {
			local[stack.ID] = stack
		}
	}
	if !s.startup.ready.Load() {
		for id := range local {
			s.startup.pending.Store(id, startupCandidate{})
		}
		s.startup.ready.Store(true)
	} else if s.peerWorkloads != nil {
		if err := s.stopFormerWorkloads(ctx, local); err != nil {
			s.log.Warn("former workload shutdown deferred", zap.Error(err))
		}
	}
	s.startup.pending.Range(func(key, candidate any) bool {
		id := key.(string)
		stack := local[id]
		if stack == nil {
			s.startup.pending.Delete(id)
		} else if err := s.restoreWorkloadOnStartup(ctx, stack, candidate.(startupCandidate)); err != nil {
			s.log.Debug("workload startup deferred", zap.String("stack_id", id), zap.Error(err))
		}
		return ctx.Err() == nil
	})
	return ctx.Err()
}

func (s *Server) restoreWorkloadOnStartup(ctx context.Context, stack *config.Stack, candidate startupCandidate) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	id, node := stack.ID, s.cfg.LocalNodeID
	status, err := s.checkStartupPeers(ctx, id)
	if err != nil {
		return err
	}
	if status.Owner != "" && status.Owner != node {
		err := s.stopFormerWorkload(ctx, stack, moveOwnerRecord{node: status.Owner, generation: status.Generation})
		if err == nil {
			s.startup.pending.Delete(id)
		}
		return err
	}
	if status.Running {
		return errors.New("workload is running on another lab node")
	}
	service, hasService, err := ipam.StackService(ctx, s.db, id)
	if err != nil {
		return err
	}
	spec, err := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
	if err != nil {
		return err
	}
	localRunning, err := s.workloadRunning(ctx, id)
	if err != nil {
		return err
	}
	if !localRunning {
		if err := s.probeStartupAddresses(ctx, spec, service.Address); err != nil {
			return err
		}
	}
	state, ok := s.raft.FSM().State(id)
	active, err := s.ops.ActiveForStack(ctx, id)
	if err != nil {
		return err
	}
	if active != nil {
		// Let durable work finish, then see whether the workload still needs to start.
		candidate.checked = true
		s.startup.pending.Store(id, candidate)
		return nil
	}
	s.startup.pending.Delete(id)
	if (localRunning && !candidate.resuming) || !ok || state.State != run.StateRunningPrimary {
		return nil
	}
	if hasService {
		_, err = s.hosts.Execute(ctx, node, "net.service.add", service)
	}
	var existing bool
	if err == nil {
		var raw json.RawMessage
		raw, err = s.hosts.Execute(ctx, node, "docker.startup.restore", ipc.DockerListPayload{StackID: id})
		if err == nil {
			err = json.Unmarshal(raw, &existing)
		}
	}
	if err == nil && !existing {
		address, _, _ := strings.Cut(service.Address, "/")
		runtime, parseErr := compose.RuntimeYAML(stack.ComposeYAML, stack.Name, address)
		err = parseErr
		if err == nil {
			_, err = s.hosts.Execute(ctx, node, "compose.up", ipc.ComposeUpPayload{StackID: id, DatasetPath: stack.ZFSDataset, ComposeFile: runtime})
		}
	}
	if err != nil {
		s.startup.pending.Store(id, startupCandidate{resuming: true})
	}
	return err
}

func (s *Server) stopFormerWorkloads(ctx context.Context, local map[string]*config.Stack) error {
	owners, err := stackMoveOwners(ctx, s.db, "")
	if err != nil {
		return err
	}
	for id, shared := range s.cachedSharedMoveOwners(ctx, "") {
		if shared.generation >= owners[id].generation {
			owners[id] = shared
		}
	}
	for id, owner := range owners {
		if stack := local[id]; stack != nil && owner.node != s.cfg.LocalNodeID {
			err = errors.Join(err, s.stopFormerWorkload(ctx, stack, owner))
		}
	}
	return err
}

func (s *Server) stopFormerWorkload(ctx context.Context, stack *config.Stack, owner moveOwnerRecord) error {
	// Retain the highest observed generation if the lab goes offline again.
	id := stack.ID
	generation, _, err := stackMoveOwner(ctx, s.db, id)
	if err != nil {
		return err
	}
	if owner.generation > generation {
		if err := s.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO workload_move_owners(stack_id,node_id,generation) VALUES(?,?,?)
			ON CONFLICT(stack_id) DO UPDATE SET node_id=excluded.node_id,generation=excluded.generation
			WHERE excluded.generation>workload_move_owners.generation`, Params: []any{id, owner.node, owner.generation}}}); err != nil {
			return err
		}
	}
	local, err := s.IsLocalWorkloadOwner(ctx, id)
	if err != nil || local {
		return err
	}
	running, err := s.workloadRunning(ctx, id)
	if err != nil || !running {
		return err
	}
	_, err = s.hosts.Execute(ctx, s.cfg.LocalNodeID, "compose.down", ipc.ComposeDownPayload{StackID: id, DatasetPath: stack.ZFSDataset})
	return err
}

func (s *Server) probeStartupAddresses(ctx context.Context, spec *compose.LifecycleSpec, serviceAddress string) error {
	addresses := map[ipc.WorkloadAddressProbe]bool{}
	if serviceAddress != "" {
		address, _, _ := strings.Cut(serviceAddress, "/")
		addresses[ipc.WorkloadAddressProbe{Address: address}] = true
	}
	for _, service := range spec.Project.Services {
		for name, endpoint := range service.Networks {
			net := spec.Project.Networks[name]
			if endpoint == nil || (!bool(net.External) && net.Driver != "macvlan" && net.Driver != "ipvlan") {
				continue
			}
			for _, address := range []string{endpoint.Ipv4Address, endpoint.Ipv6Address} {
				if address == "" {
					continue
				}
				probe := ipc.WorkloadAddressProbe{Address: address, Interface: net.DriverOpts["parent"]}
				if bool(net.External) {
					probe.DockerNetwork = net.Name
				}
				addresses[probe] = true
			}
		}
	}
	for probe := range addresses {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		raw, err := s.hosts.Execute(probeCtx, s.cfg.LocalNodeID, "net.address.probe", probe)
		cancel()
		if err != nil {
			return err
		}
		var used bool
		if err := json.Unmarshal(raw, &used); err != nil {
			return err
		}
		if used {
			return fmt.Errorf("a remote MAC responds for workload address %s", probe.Address)
		}
	}
	return nil
}

func (s *Server) workloadRunning(ctx context.Context, id string) (bool, error) {
	raw, err := s.hosts.Execute(ctx, s.cfg.LocalNodeID, "docker.list", ipc.DockerListPayload{StackID: id})
	if err != nil {
		return false, err
	}
	var result ipc.DockerListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, err
	}
	for _, c := range result.Containers {
		if c.State == "running" || c.State == "restarting" || c.State == "paused" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) startupOwner(ctx context.Context, id string) (workloadStartupStatus, error) {
	generation, owner, err := stackMoveOwner(ctx, s.db, id)
	status := workloadStartupStatus{Owner: owner, Generation: generation}
	if err != nil {
		return status, err
	}
	shared := s.cachedSharedMoveOwners(ctx, id)[id]
	err = status.merge(workloadStartupStatus{Owner: shared.node, Generation: shared.generation})
	return status, err
}

func (s *Server) checkStartupPeers(ctx context.Context, id string) (workloadStartupStatus, error) {
	status, err := s.startupOwner(ctx, id)
	if err != nil || s.peerWorkloads == nil {
		return status, err
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	strong, err := sharedStackOwner(probe, s.labDatabase(), id)
	cancel()
	if err == nil {
		if err := status.merge(workloadStartupStatus{Owner: strong.node, Generation: strong.generation}); err != nil {
			return status, err
		}
	}
	if err := s.eachReplica(ctx, func(replica *rqlite.Client, _ rqlite.LocalIdentity, _ time.Time) error {
		generation, owner, err := stackMoveOwner(ctx, replica, id)
		if err != nil {
			return err
		}
		return status.merge(workloadStartupStatus{Owner: owner, Generation: generation})
	}); err != nil {
		return status, err
	}
	if s.startup.peers != nil {
		peers := s.startup.peers()
		results := make(chan workloadStartupStatus, len(peers))
		for _, peer := range peers {
			go func() { results <- s.probeStartupPeer(ctx, peer, id) }()
		}
		for range peers {
			if err := status.merge(<-results); err != nil {
				return status, err
			}
		}
	}
	return status, ctx.Err()
}

func (s *Server) probeStartupPeer(ctx context.Context, peer, id string) workloadStartupStatus {
	var status workloadStartupStatus
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.backupPeerRequest(probe, peer, http.MethodGet, workloadStartupPath+id, nil, &status); err != nil {
		return workloadStartupStatus{}
	}
	return status
}

func (status *workloadStartupStatus) merge(other workloadStartupStatus) error {
	if other.Owner != "" {
		if other.Generation == status.Generation && status.Owner != "" && other.Owner != status.Owner {
			return errors.New("conflicting workload ownership prevents startup")
		}
		if other.Generation > status.Generation || status.Owner == "" {
			status.Owner, status.Generation = other.Owner, other.Generation
		}
	}
	status.Running = status.Running || other.Running
	return nil
}

// ServeWorkloadStartupPeer is mounted only on the authenticated lab listener.
func (s *Server) ServeWorkloadStartupPeer(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, workloadStartupPath)
	if _, err := uuid.Parse(id); r.Method != http.MethodGet || err != nil {
		http.NotFound(w, r)
		return
	}
	status, err := s.startupOwner(r.Context(), id)
	if err == nil {
		status.Running, err = s.workloadRunning(r.Context(), id)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}
