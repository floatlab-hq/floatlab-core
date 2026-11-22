package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"time"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type replicaFailoverRequest struct {
	Action            string `json:"action"`
	DestinationNodeID string `json:"destination_node_id"`
	PrimaryFenced     bool   `json:"primary_fenced"`
	RecoveryPoint     string `json:"recovery_point"`
	Name              string `json:"name"`
	NetworkPool       string `json:"network_pool"`
}

type replicaFailoverPreview struct {
	StackID           string                         `json:"stack_id"`
	SourceNodeID      string                         `json:"source_node_id"`
	DestinationNodeID string                         `json:"destination_node_id"`
	Mode              string                         `json:"mode"`
	RecoveryPoint     string                         `json:"recovery_point"`
	Snapshot          string                         `json:"snapshot"`
	SnapshotAt        time.Time                      `json:"snapshot_at"`
	PotentialDataLoss int64                          `json:"potential_data_loss_seconds"`
	RequiresNetwork   bool                           `json:"requires_network"`
	Datasets          []worker.BackupRestoreSnapshot `json:"datasets"`
	stack             *config.Stack
	ownerGeneration   int64
	guids             map[string]string
	sourceAddress     string
	currentPool       string
}

func (s *Server) routeFailoverDestination(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.peerWorkloads == nil {
			next.ServeHTTP(w, r)
			return
		}
		destination := r.URL.Query().Get("destination_node_id")
		if r.Method == http.MethodPost {
			var req replicaFailoverRequest
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if err != nil || len(bytes.TrimSpace(body)) != 0 && json.Unmarshal(body, &req) != nil {
				writeError(w, 400, "invalid failover request")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if req.Action == "restore" || req.DestinationNodeID == "" {
				next.ServeHTTP(w, r)
				return
			}
			destination = req.DestinationNodeID
		}
		if destination == "" {
			writeError(w, 400, "destination_node_id required")
			return
		}
		if destination == s.cfg.LocalNodeID {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-FloatLab-Workload-Hop") != "" {
			writeError(w, 503, "failover destination is unavailable")
			return
		}
		s.forwardWorkloadOwner(w, r, destination)
	})
}

func (s *Server) handleFailoverPreview(w http.ResponseWriter, r *http.Request) {
	if s.peerWorkloads == nil || s.cfg.LabDB == nil {
		writeError(w, 409, "replica failover requires an active lab")
		return
	}
	preview, err := s.previewReplicaFailover(r.Context(), chi.URLParam(r, "stack_id"))
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, preview)
}

func (s *Server) handleReplicaFailover(w http.ResponseWriter, r *http.Request, id string) {
	var req replicaFailoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); (err != nil && err != io.EOF) || (req.Action != "" && req.Action != "trigger" && req.Action != "restore") {
		writeError(w, 400, "invalid failover request")
		return
	}
	if req.Action == "restore" {
		s.submitFailover(w, r, id, true)
		return
	}
	if req.DestinationNodeID == "" {
		writeErrorCode(w, http.StatusConflict, "coordinated_takeover_required", "cross-node takeover requires a nominated destination and primary fencing confirmation")
		return
	}
	if s.cfg.LabDB == nil || req.DestinationNodeID != s.cfg.LocalNodeID {
		writeError(w, 409, "failover must be accepted by its destination node")
		return
	}
	if !req.PrimaryFenced || req.RecoveryPoint == "" {
		writeError(w, 400, "confirm the primary is fenced and provide the preview recovery_point")
		return
	}
	ctx := r.Context()
	preview, err := s.previewReplicaFailover(ctx, id)
	if err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if req.RecoveryPoint != preview.RecoveryPoint {
		writeError(w, 409, "the recovery point changed; review a fresh failover preview")
		return
	}
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	var running struct{ Running bool }
	_ = s.backupPeerRequest(probe, preview.SourceNodeID, http.MethodGet, workloadStartupPath+id, nil, &running)
	cancel()
	if running.Running {
		writeError(w, 409, "the original primary still reports running containers")
		return
	}
	pool := ""
	if preview.RequiresNetwork {
		pool, err = s.failoverNetworkPool(ctx, req.NetworkPool, preview)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	stack := *preview.stack
	stack.PrimaryNodeID = s.cfg.LocalNodeID
	stack.UpdatedAt = time.Now().UTC()
	if preview.Mode == "clone" {
		name, err := compose.Slug(req.Name)
		if err != nil || name == stack.Name || name == "system" || name == "backups" || name == "restores" {
			writeError(w, 400, "a different, valid stack name is required for a failover clone")
			return
		}
		stack.ID, stack.Name, stack.ZFSDataset = uuid.NewString(), name, "floatlab/"+name
		stack.BackupNodeID, stack.FailoverMode, stack.AutoTriggerAfter = "", "manual", ""
		stack.SnapshotSchedule, stack.ReplicationSchedule, stack.BackupSchedule, stack.BackupTarget = "", "", "", ""
		stack.CreatedAt = stack.UpdatedAt
		stack.ComposeYAML, err = compose.RestoreSource(preview.stack.ComposeYAML, preview.stack.Name, name, stack.PrimaryNodeID)
		if err == nil && pool != "" {
			stack.ComposeYAML, err = setFailoverNetworkPool(stack.ComposeYAML, pool)
		}
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
	} else {
		if req.Name != "" {
			writeError(w, 400, "a takeover retains the original stack name")
			return
		}
		stack.BackupNodeID = ""
		stack.ComposeYAML, err = movedCompose(stack.ComposeYAML, stack.PrimaryNodeID, "", pool)
		if err != nil {
			writeError(w, 409, err.Error())
			return
		}
	}
	s.moveNames.Lock()
	defer s.moveNames.Unlock()
	reservations, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT 1 FROM workload_move_reservations WHERE stack_id=? OR name=?`, Params: []any{stack.ID, stack.Name}})
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	if len(reservations.Values) != 0 {
		writeError(w, 409, "the destination stack name or ID is already reserved")
		return
	}
	if err := s.checkMoveDestination(ctx, stackMove{StackID: stack.ID, Stack: stack, Destination: stack.PrimaryNodeID}); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	payload := worker.BackupRestorePayload{SourceNodeID: preview.SourceNodeID, BackupNodeID: stack.PrimaryNodeID, Snapshots: preview.Datasets, ExpectedGUIDs: preview.guids, OwnerGeneration: preview.ownerGeneration}
	s.queueBackupRestore(w, r, &stack, payload)
}

func setFailoverNetworkPool(source, pool string) (string, error) {
	var document map[string]any
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", err
	}
	document["x-fl-network-pool"] = pool
	raw, err := yaml.Marshal(document)
	return string(raw), err
}

func (s *Server) failoverNetworkPool(ctx context.Context, requested string, preview replicaFailoverPreview) (string, error) {
	name, err := s.moveNetworkPool(ctx, preview.DestinationNodeID, requested, preview.currentPool)
	if err != nil {
		return "", err
	}
	pools, err := ipam.ListPools(ctx, s.db)
	if err != nil {
		return "", err
	}
	pool, err := ipam.SelectPool(pools, name, "", preview.DestinationNodeID)
	if err != nil {
		return "", err
	}
	if ipam.Member(*pool, preview.SourceNodeID) || len(pool.NodeIDs) != 1 {
		return "", fmt.Errorf("failover requires a pool owned exclusively by the destination")
	}
	old, err := netip.ParseAddr(preview.sourceAddress)
	if err == nil {
		start, _ := netip.ParseAddr(pool.StartIP)
		end, _ := netip.ParseAddr(pool.EndIP)
		if old.Compare(start) >= 0 && old.Compare(end) <= 0 {
			return "", fmt.Errorf("the destination pool contains the original stack IP")
		}
	}
	return name, nil
}

func (s *Server) previewReplicaFailover(ctx context.Context, id string) (replicaFailoverPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	preview := replicaFailoverPreview{StackID: id, DestinationNodeID: s.cfg.LocalNodeID, Mode: "clone"}
	if uuid.Validate(id) != nil {
		return preview, fmt.Errorf("invalid stack ID")
	}
	nodes, err := s.store.ListNodes(ctx)
	if err != nil {
		return preview, err
	}
	active := 0
	for _, node := range nodes {
		if _, ok := s.peerWorkloads.lookup(node.ID); ok || node.ID == s.cfg.LocalNodeID {
			active++
		}
	}
	if active < 2 {
		return preview, fmt.Errorf("failover requires at least two active lab members")
	}
	if active > 2 {
		preview.Mode = "takeover"
	}
	var sourceGuard bool
	var generation int64
	err = s.eachReplica(ctx, func(db *rqlite.Client, identity rqlite.LocalIdentity, _ time.Time) error {
		stack, err := config.NewStore(db).GetStack(ctx, id)
		if err != nil {
			return nil
		}
		if stack.PrimaryNodeID != identity.NodeID || stack.BackupNodeID != s.cfg.LocalNodeID {
			return nil
		}
		if _, ok := s.peerWorkloads.lookup(identity.NodeID); !ok {
			return nil
		}
		if preview.stack != nil {
			return fmt.Errorf("multiple replicas claim to be the primary")
		}
		preview.stack, preview.SourceNodeID = stack, identity.NodeID
		generation, _, err = stackMoveOwner(ctx, db, id)
		if err != nil {
			return err
		}
		if preview.Mode == "takeover" {
			sourceGuard, err = db.LocalMigrationDone(ctx, workloadStartupMigration)
			if err != nil {
				return err
			}
		}
		rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT stack_ip FROM stack_runtime WHERE stack_id=? AND deleted_at IS NULL`, Params: []any{id}})
		if err == nil && len(rows.Values) == 1 {
			preview.sourceAddress, _ = rows.Values[0][0].(string)
		}
		return err
	})
	if err != nil {
		return preview, err
	}
	if preview.stack == nil {
		return preview, fmt.Errorf("no published stack configuration nominates this secondary")
	}
	if owner := s.cachedSharedMoveOwners(ctx, id)[id]; owner.node != "" && owner.node != preview.SourceNodeID {
		return preview, fmt.Errorf("the published configuration does not match the current owner")
	}
	if preview.Mode == "takeover" {
		if !sourceGuard {
			return preview, fmt.Errorf("the primary has not published proof of managed workload startup support")
		}
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		owner, err := sharedStackOwner(probe, s.cfg.LabDB, id)
		if err != nil {
			return preview, fmt.Errorf("lab quorum is required for takeover: %w", err)
		}
		if owner.node != "" && owner.node != preview.SourceNodeID {
			return preview, fmt.Errorf("the published configuration does not match the current owner")
		}
		preview.ownerGeneration = max(generation, owner.generation) + 1
	}
	stack := preview.stack
	spec, err := compose.ParseRestoreSource(stack.ComposeYAML, stack.Name)
	if err != nil {
		return preview, err
	}
	if stack.ZFSDataset != "floatlab/"+stack.Name || stack.Name == "system" || stack.Name == "backups" || stack.Name == "restores" || len(spec.Mounts) == 0 {
		return preview, fmt.Errorf("failover requires a managed stack with at least one replicated mount")
	}
	if err := validateReplicaFailoverProject(spec, stack.ZFSDataset); err != nil {
		return preview, err
	}
	preview.RequiresNetwork, preview.currentPool = spec.HasPorts, spec.NetworkPool
	points, err := s.replicaRecoveryPoints(ctx, preview.SourceNodeID, spec)
	if err != nil {
		return preview, err
	}
	for snapshot := range points {
		if snapshot > preview.Snapshot && len(points[snapshot]) == len(spec.Mounts) {
			preview.Snapshot = snapshot
		}
	}
	if preview.Snapshot == "" {
		return preview, fmt.Errorf("no complete recovery point covers every writable mount")
	}
	preview.guids = points[preview.Snapshot]
	for dataset := range preview.guids {
		preview.Datasets = append(preview.Datasets, worker.BackupRestoreSnapshot{Dataset: dataset, Snapshot: preview.Snapshot})
	}
	sort.Slice(preview.Datasets, func(i, j int) bool { return preview.Datasets[i].Dataset < preview.Datasets[j].Dataset })
	preview.SnapshotAt, _ = store.DecodeSnapshotTimestamp(preview.Snapshot[3:])
	preview.PotentialDataLoss = max(0, int64(time.Since(preview.SnapshotAt)/time.Second))
	bound, err := json.Marshal([]any{id, preview.SourceNodeID, preview.DestinationNodeID, preview.Mode, preview.ownerGeneration, preview.Snapshot, preview.guids, stack.ComposeYAML, preview.sourceAddress})
	if err != nil {
		return preview, err
	}
	preview.RecoveryPoint = fmt.Sprintf("%x", sha256.Sum256(bound))
	return preview, nil
}

func (s *Server) replicaRecoveryPoints(ctx context.Context, source string, spec *compose.LifecycleSpec) (map[string]map[string]string, error) {
	points := map[string]map[string]string{}
	for dataset := range spec.Mounts {
		raw, err := s.hosts.Execute(ctx, s.cfg.LocalNodeID, "fs.backup.recovery-points", ipc.BackupRecoveryPointsPayload{SourceNodeID: source, Dataset: dataset})
		if err != nil {
			return nil, fmt.Errorf("verify the backup for %s: %w", dataset, err)
		}
		var snapshots []store.BackupSnapshot
		if err := json.Unmarshal(raw, &snapshots); err != nil {
			return nil, err
		}
		for _, snapshot := range snapshots {
			if !store.IsAutomatedSnapshot(snapshot.Name) || snapshot.GUID == "" {
				continue
			}
			if points[snapshot.Name] == nil {
				points[snapshot.Name] = map[string]string{}
			}
			points[snapshot.Name][dataset] = snapshot.GUID
		}
	}
	return points, nil
}

func validateReplicaFailoverProject(spec *compose.LifecycleSpec, root string) error {
	if err := validateMoveProject(spec, root); err != nil {
		return err
	}
	for _, network := range spec.Project.Networks {
		if network.Driver != "" && network.Driver != "bridge" {
			return fmt.Errorf("failover cannot allocate an isolated address for network %s using %s", network.Name, network.Driver)
		}
	}
	for _, service := range spec.Project.Services {
		if service.MacAddress != "" {
			return fmt.Errorf("failover cannot reuse a fixed MAC address for service %s", service.Name)
		}
		for _, network := range service.Networks {
			if network != nil && network.MacAddress != "" {
				return fmt.Errorf("failover cannot reuse a fixed network MAC for service %s", service.Name)
			}
		}
	}
	return nil
}
