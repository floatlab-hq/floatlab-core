package control

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
)

func registerStorageRoutes(r chi.Router, s *Server) {
	r.Get("/storage/pools", s.handleListPools)
	r.Get("/storage/pools/{node_id}/{pool}", s.handleGetPool)
	r.Get("/storage/datasets/{stack_id}", s.handleGetStackDataset)
	r.Get("/storage/datasets/{stack_id}/snapshots", s.handleListSnapshots)
	r.Post("/storage/datasets/{stack_id}/snapshots", s.handleCreateSnapshot)
	r.Delete("/storage/datasets/{stack_id}/snapshots/{name}", s.handleDeleteSnapshot)
	r.Post("/storage/replication/{stack_id}/trigger", s.handleTriggerReplication)
	r.Get("/storage/replication", s.handleListReplication)
	r.Get("/storage/faults", s.handleListFaults)
}

// poolResponse maps to the frontend ZfsPool type.
type poolResponse struct {
	NodeID     string        `json:"node_id"`
	Name       string        `json:"name"`
	State      string        `json:"state"`
	SizeBytes  int64         `json:"size_bytes"`
	AllocBytes int64         `json:"alloc_bytes"`
	FreeBytes  int64         `json:"free_bytes"`
	CapPct     float64       `json:"cap_pct"`
	Health     string        `json:"health"`
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
		vdevs = append(vdevs, vdevResponse{Name: v.Name, State: v.State})
	}
	writeJSON(w, http.StatusOK, poolResponse{
		NodeID: nodeID,
		Name:   result.Name,
		State:  result.Health,
		Health: result.Health,
		VDevs:  vdevs,
	})
}

// datasetResponse maps to the frontend ZfsDataset type.
type datasetResponse struct {
	StackID    string `json:"stack_id"`
	Name       string `json:"name"`
	UsedBytes  int64  `json:"used_bytes"`
	AvailBytes int64  `json:"avail_bytes"`
	QuotaBytes *int64 `json:"quota_bytes"`
	Mountpoint string `json:"mount_point"`
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
				StackID:    stackID,
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

	snaps := make([]snapshotResponse, 0, len(result.Snapshots))
	for _, sn := range result.Snapshots {
		snaps = append(snaps, snapshotResponse{
			StackID:   stackID,
			Dataset:   sn.Dataset,
			Name:      sn.Name,
			Type:      string(store.ClassifySnapshot(sn.Name)),
			CreatedAt: sn.CreatedAt,
			SizeBytes: sn.Used,
		})
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
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Name == "" {
		body.Name = store.UserSnapshotName("manual")
	}

	taskID := uuid.New().String()
	payload := worker.SnapshotCreatePayload{
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
	writeJSON(w, http.StatusAccepted, map[string]string{"task_id": taskID, "snapshot": body.Name})
}

func (s *Server) handleDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	snapName := chi.URLParam(r, "name")

	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	taskID := uuid.New().String()
	payload := worker.SnapshotDeletePayload{
		Dataset:  st.ZFSDataset,
		NodeID:   st.PrimaryNodeID,
		Snapshot: snapName,
	}
	if err := worker.EnqueueTask(r.Context(), s.db, taskID, worker.TaskSnapshotDelete, stackID, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleTriggerReplication(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if st.BackupNodeID == "" {
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
	// Return pending/running replication tasks from the task queue.
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT id, stack_id, state, created_at, updated_at, error FROM tasks
		      WHERE type = 'repl.trigger' ORDER BY created_at DESC LIMIT 100`,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type replJob struct {
		ID          string  `json:"id"`
		StackID     string  `json:"stack_id"`
		SourceNode  string  `json:"source_node"`
		DestNode    string  `json:"dest_node"`
		State       string  `json:"state"`
		BytesSent   int64   `json:"bytes_sent"`
		BytesTotal  int64   `json:"bytes_total"`
		StartedAt   string  `json:"started_at"`
		CompletedAt *string `json:"completed_at"`
		Error       *string `json:"error"`
	}

	jobs := make([]replJob, 0, len(result.Values))
	for _, row := range result.Values {
		job := replJob{}
		job.ID, _ = row[0].(string)
		job.StackID, _ = row[1].(string)
		job.State, _ = row[2].(string)
		job.StartedAt, _ = row[3].(string)
		if errStr, ok := row[5].(string); ok && errStr != "" {
			job.Error = &errStr
		}
		jobs = append(jobs, job)
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) handleListFaults(w http.ResponseWriter, r *http.Request) {
	// Return active ZFS fault alerts.
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT id, node_id, severity, message, created_at FROM alerts
		      WHERE kind = 'zfs_fault' AND state = 'active' ORDER BY created_at DESC`,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type fault struct {
		ID        string `json:"id"`
		NodeID    string `json:"node_id"`
		Severity  string `json:"severity"`
		Message   string `json:"message"`
		CreatedAt string `json:"created_at"`
	}

	faults := make([]fault, 0, len(result.Values))
	for _, row := range result.Values {
		f := fault{}
		f.ID, _ = row[0].(string)
		f.NodeID, _ = row[1].(string)
		f.Severity, _ = row[2].(string)
		f.Message, _ = row[3].(string)
		f.CreatedAt, _ = row[4].(string)
		faults = append(faults, f)
	}
	writeJSON(w, http.StatusOK, faults)
}
