package control

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func registerStorageRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Use(s.idempotency)
		r.Get("/storage/pools", s.handleListPools)
		r.Get("/storage/pools/{node_id}/{pool}", s.handleGetPool)
		r.Get("/storage/datasets", s.handleListDatasets)
		r.Post("/storage/datasets/{stack_id}/snapshots/{name}/restore", s.handleRollbackDataset)
		r.Get("/storage/datasets/{stack_id}", s.handleGetStackDataset)
		r.Get("/storage/datasets/{stack_id}/snapshots", s.handleListSnapshots)
		r.Post("/storage/datasets/{stack_id}/snapshots", s.handleCreateSnapshot)
		r.Delete("/storage/datasets/{stack_id}/snapshots/{name}", s.handleDeleteSnapshot)
		r.Post("/storage/replication/{stack_id}/trigger", s.handleTriggerReplication)
		r.Get("/storage/replication", s.handleListReplication)
		r.Get("/storage/faults", s.handleListFaults)
	})
}

// poolResponse maps to the frontend ZfsPool type.
type poolResponse struct {
	CreatedAt  string         `json:"created_at"`
	NodeID     string         `json:"node_id"`
	Name       string         `json:"name"`
	State      string         `json:"state"`
	SizeBytes  int64          `json:"size_bytes"`
	AllocBytes int64          `json:"alloc_bytes"`
	FreeBytes  int64          `json:"free_bytes"`
	CapPct     float64        `json:"cap_pct"`
	Health     string         `json:"health"`
	VDevs      []vdevResponse `json:"vdevs"`
}

type vdevResponse struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	State string `json:"state"`
}

func (s *Server) handleListPools(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var mu sync.Mutex
	var results []poolResponse
	var wg sync.WaitGroup

	for _, node := range nodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			raw, err := s.hosts.Execute(r.Context(), nodeID, "fs.pool.list", struct{}{})
			if err != nil {
				s.log.Warn("handleListPools: node unreachable", zap.String("node", nodeID))
				return
			}
			var result ipc.PoolListResult
			if err := json.Unmarshal(raw, &result); err != nil {
				return
			}
			mu.Lock()
			for _, p := range result.Pools {
				var cap float64
				if total := p.Used + p.Available; total > 0 {
					cap = float64(p.Used) / float64(total) * 100
				}
				results = append(results, poolResponse{
					NodeID:     nodeID,
					CreatedAt:  p.CreatedAt,
					Name:       p.Name,
					State:      p.Health,
					SizeBytes:  p.Used + p.Available,
					AllocBytes: p.Used,
					FreeBytes:  p.Available,
					CapPct:     cap,
					Health:     p.Health,
					VDevs:      []vdevResponse{},
				})
			}
			mu.Unlock()
		}(node.ID)
	}
	wg.Wait()

	if results == nil {
		results = []poolResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	pool := chi.URLParam(r, "pool")

	raw, err := s.hosts.Execute(r.Context(), nodeID, "fs.pool.health", ipc.PoolHealthPayload{Pool: pool})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var result ipc.PoolHealthResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	vdevs := make([]vdevResponse, 0, len(result.VDevs))
	for _, v := range result.VDevs {
		vdevs = append(vdevs, vdevResponse{Name: v.Name, State: v.State, Type: "disk"})
	}
	rawPools, err := s.hosts.Execute(r.Context(), nodeID, "fs.pool.list", struct{}{})
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var pools ipc.PoolListResult
	if err := json.Unmarshal(rawPools, &pools); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var summary ipc.PoolSummaryResult
	for _, item := range pools.Pools {
		if item.Name == pool {
			summary = item
			break
		}
	}
	writeJSON(w, http.StatusOK, poolResponse{
		CreatedAt: summary.CreatedAt, SizeBytes: summary.Used + summary.Available, AllocBytes: summary.Used, FreeBytes: summary.Available,
		NodeID: nodeID,
		Name:   result.Name,
		State:  result.Health,
		Health: result.Health,
		VDevs:  vdevs,
	})
}

// datasetResponse maps to the frontend ZfsDataset type.
type datasetResponse struct {
	Pool            string `json:"pool"`
	NodeID          string `json:"node_id"`
	CreatedAt       string `json:"created_at"`
	MountpointAlias string `json:"mountpoint"`
	StackID         string `json:"stack_id"`
	Name            string `json:"name"`
	UsedBytes       int64  `json:"used_bytes"`
	AvailBytes      int64  `json:"avail_bytes"`
	QuotaBytes      *int64 `json:"quota_bytes"`
	Mountpoint      string `json:"mount_point"`
}

func (s *Server) handleGetStackDataset(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	nodeID := st.PrimaryNodeID
	raw, err := s.hosts.Execute(r.Context(), nodeID, "fs.dataset.list", ipc.DatasetListPayload{Parent: st.ZFSDataset})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var result ipc.DatasetListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Return the root dataset (first entry matching ZFSDataset exactly).
	for _, ds := range result.Datasets {
		if ds.Name == st.ZFSDataset {
			var quota *int64
			if ds.Quota > 0 {
				q := ds.Quota
				quota = &q
			}
			writeJSON(w, http.StatusOK, datasetResponse{
				StackID: stackID,
				Pool:    strings.SplitN(ds.Name, "/", 2)[0], NodeID: nodeID, CreatedAt: ds.CreatedAt, MountpointAlias: ds.Mountpoint,
				Name:       ds.Name,
				UsedBytes:  ds.Used,
				AvailBytes: ds.Available,
				QuotaBytes: quota,
				Mountpoint: ds.Mountpoint,
			})
			return
		}
	}
	writeError(w, http.StatusNotFound, "dataset not found: "+st.ZFSDataset)
}

// snapshotResponse maps to the frontend Snapshot type.
type snapshotResponse struct {
	NodeID          string `json:"node_id"`
	UsedBytes       int64  `json:"used_bytes"`
	Kind            string `json:"kind"`
	StackID         string `json:"stack_id"`
	Dataset         string `json:"dataset"`
	Name            string `json:"name"`
	Type            string `json:"type"`
	CreatedAt       string `json:"created_at"`
	SizeBytes       int64  `json:"size_bytes"`
	ReferencedBytes int64  `json:"referenced_bytes"`
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	nodeID := st.PrimaryNodeID
	raw, err := s.hosts.Execute(r.Context(), nodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: st.ZFSDataset})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	limit, err := boundedLimit(r, 50, 500)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && kind != "user" && kind != "scheduled" && kind != "replication" {
		writeError(w, 400, "invalid snapshot kind")
		return
	}
	snaps := make([]snapshotResponse, 0, len(result.Snapshots))
	for _, sn := range result.Snapshots {
		if kind != "" && string(store.ClassifySnapshot(sn.Name)) != kind {
			continue
		}
		snaps = append(snaps, snapshotResponse{
			StackID: stackID,
			NodeID:  nodeID, UsedBytes: sn.Used, Kind: string(store.ClassifySnapshot(sn.Name)),
			Dataset:   sn.Dataset,
			Name:      sn.Name,
			Type:      string(store.ClassifySnapshot(sn.Name)),
			CreatedAt: sn.CreatedAt,
			SizeBytes: sn.Used,
		})
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].CreatedAt > snaps[j].CreatedAt })
	if len(snaps) > limit {
		snaps = snaps[:limit]
	}
	writeJSON(w, http.StatusOK, snaps)
}

func (s *Server) handleCreateSnapshot(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var body struct {
		Name      string `json:"name"`
		Label     string `json:"label"`
		Recursive bool   `json:"recursive"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Label != "" {
		body.Name = body.Label
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`).MatchString(body.Name) {
		writeError(w, 400, "a valid snapshot label is required")
		return
	}

	taskID := uuid.New().String()
	opID := operationID(r.Context())
	snapshot := store.UserSnapshotName(body.Name)
	raw, err := s.hosts.Execute(r.Context(), st.PrimaryNodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: st.ZFSDataset})
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var listed ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	for _, sn := range listed.Snapshots {
		if sn.Name == snapshot && sn.Dataset == st.ZFSDataset {
			writeError(w, 409, "snapshot already exists")
			return
		}
	}
	payload := worker.SnapshotCreatePayload{
		OperationID: opID, StackID: stackID, Name: snapshot, Recursive: body.Recursive,
		Dataset:  st.ZFSDataset,
		NodeID:   st.PrimaryNodeID,
		SnapType: "user",
		Label:    body.Name,
		Keep:     0,
	}
	if err := worker.EnqueueTask(r.Context(), s.db, taskID, worker.TaskSnapshotCreate, stackID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]interface{}{"task_id": taskID, "snapshot": snapshot, "operation_id": opID, "name": snapshot, "dataset": st.ZFSDataset, "node_id": st.PrimaryNodeID, "stack_id": stackID, "used_bytes": 0, "created_at": time.Now().UTC().Format(time.RFC3339Nano), "kind": "user", "state": "pending"})
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	snapName := chi.URLParam(r, "name")

	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	raw, err := s.hosts.Execute(r.Context(), st.PrimaryNodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: st.ZFSDataset})
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var listed ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	found := false
	for _, snapshot := range listed.Snapshots {
		if snapshot.Name == snapName && snapshot.Dataset == st.ZFSDataset {
			found = true
		}
	}
	if !found {
		writeError(w, 404, "snapshot not found")
		return
	}
	if strings.HasPrefix(snapName, "fsrepl-") {
		jobs, err := s.queryObjects(r, `SELECT id FROM tasks WHERE stack_id=? AND type='repl.trigger' AND state IN ('pending','running')`, stackID)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if len(jobs) > 0 {
			writeError(w, 409, "snapshot is in use by replication")
			return
		}
	}
	taskID := uuid.New().String()
	payload := worker.SnapshotDeletePayload{
		OperationID: operationID(r.Context()), StackID: stackID,
		Dataset:  st.ZFSDataset,
		NodeID:   st.PrimaryNodeID,
		Snapshot: snapName,
	}
	if err := worker.EnqueueTask(r.Context(), s.db, taskID, worker.TaskSnapshotDelete, stackID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID(r.Context()), "task_id": taskID})
}

func (s *Server) handleTriggerReplication(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if st.BackupNodeID == "" || st.BackupNodeID == st.PrimaryNodeID {
		writeError(w, http.StatusBadRequest, "stack has no secondary node configured")
		return
	}

	// Resolve secondary node's LAN address for ZFS recv destination.
	destNode, err := s.store.GetNode(r.Context(), st.BackupNodeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "secondary node not found: "+err.Error())
		return
	}
	var destHost string
	for _, addr := range destNode.Addresses {
		if addr.Type == "LAN-6" {
			destHost = addr.Address
			break
		}
	}
	if destHost == "" {
		writeError(w, http.StatusInternalServerError, "secondary node has no LAN-6 address")
		return
	}

	taskID := uuid.New().String()
	payload := worker.ReplTriggerPayload{
		StackID:  stackID,
		Dataset:  st.ZFSDataset,
		NodeID:   st.PrimaryNodeID,
		DestHost: destHost,
		DestPort: 22,
	}
	if err := worker.EnqueueTask(r.Context(), s.db, taskID, worker.TaskReplTrigger, stackID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"task_id":    taskID,
		"stack_id":   stackID,
		"state":      "pending",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleListReplication(w http.ResponseWriter, r *http.Request) {
	limit, err := boundedLimit(r, 20, 200)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	query := `SELECT t.id,t.stack_id,coalesce(s.primary_node_id,'') AS source_node_id,coalesce(s.backup_node_id,'') AS dest_node_id,CASE t.state WHEN 'pending' THEN 'idle' WHEN 'done' THEN 'complete' ELSE t.state END AS state,t.created_at,t.created_at AS started_at,CASE WHEN t.state IN ('done','failed') THEN t.updated_at END AS completed_at,nullif(t.error,'') AS error FROM tasks t LEFT JOIN stacks s ON s.id=t.stack_id WHERE t.type='repl.trigger'`
	args := []interface{}{}
	if id := r.URL.Query().Get("stack_id"); id != "" {
		query += " AND t.stack_id=?"
		args = append(args, id)
	}
	if state := r.URL.Query().Get("state"); state != "" {
		switch state {
		case "idle":
			state = "pending"
		case "complete":
			state = "done"
		case "running", "failed":
		default:
			writeError(w, 400, "invalid replication state")
			return
		}
		query += " AND t.state=?"
		args = append(args, state)
	}
	args = append(args, limit)
	s.writeObjects(w, r, query+" ORDER BY t.created_at DESC LIMIT ?", args...)
}

func (s *Server) handleListFaults(w http.ResponseWriter, r *http.Request) {
	include, err := optionalBool(r.URL.Query().Get("include_cleared"))
	if err != nil {
		writeError(w, 400, "invalid include_cleared")
		return
	}
	query := `SELECT id,node_id,'floatlab' AS pool,severity,message,created_at AS detected_at,resolved_at AS cleared_at,created_at FROM alerts WHERE kind='zfs_fault'`
	args := []interface{}{}
	if !include {
		query += " AND state='active'"
	}
	if id := r.URL.Query().Get("node_id"); id != "" {
		query += " AND node_id=?"
		args = append(args, id)
	}
	s.writeObjects(w, r, query+" ORDER BY created_at DESC", args...)
}
