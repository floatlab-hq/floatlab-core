package control

import (
	"encoding/json"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/go-chi/chi/v5"
	"io"
	"net/http"
	"strings"
)

func (s *Server) handleListDatasets(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.store.ListStacks(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	datasets := make([]datasetResponse, 0, len(stacks))
	for _, stack := range stacks {
		if node := r.URL.Query().Get("node_id"); node != "" && stack.PrimaryNodeID != node {
			continue
		}
		if id := r.URL.Query().Get("stack_id"); id != "" && stack.ID != id {
			continue
		}
		raw, err := s.hosts.Execute(r.Context(), stack.PrimaryNodeID, "fs.dataset.list", ipc.DatasetListPayload{Parent: stack.ZFSDataset})
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		var result ipc.DatasetListResult
		if err := json.Unmarshal(raw, &result); err != nil {
			writeError(w, 502, err.Error())
			return
		}
		for _, ds := range result.Datasets {
			var quota *int64
			if ds.Quota > 0 {
				q := ds.Quota
				quota = &q
			}
			datasets = append(datasets, datasetResponse{Pool: strings.SplitN(ds.Name, "/", 2)[0], NodeID: stack.PrimaryNodeID, CreatedAt: ds.CreatedAt, MountpointAlias: ds.Mountpoint, StackID: stack.ID, Name: ds.Name, UsedBytes: ds.Used, AvailBytes: ds.Available, QuotaBytes: quota, Mountpoint: ds.Mountpoint})
		}
	}
	writeJSON(w, 200, datasets)
}

func (s *Server) handleRollbackDataset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "stack_id")
	name := chi.URLParam(r, "name")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	instance, ok := s.raft.FSM().State(id)
	if !ok || instance.State != run.StateIdle {
		writeError(w, 409, "stack must be Idle before dataset rollback")
		return
	}
	body := struct {
		DestroyNewer bool `json:"destroy_newer"`
	}{true}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeError(w, 400, "invalid JSON")
		return
	}
	raw, err := s.hosts.Execute(r.Context(), stack.PrimaryNodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: stack.ZFSDataset})
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var target *ipc.SnapshotInfoResult
	for i := range result.Snapshots {
		if result.Snapshots[i].Dataset == stack.ZFSDataset && result.Snapshots[i].Name == name {
			target = &result.Snapshots[i]
			break
		}
	}
	if target == nil {
		writeError(w, 404, "snapshot not found")
		return
	}
	if !body.DestroyNewer {
		for _, snapshot := range result.Snapshots {
			if snapshot.Dataset == stack.ZFSDataset && snapshotNewer(snapshot, *target) {
				writeError(w, 409, "newer snapshots exist")
				return
			}
		}
	}
	opID := operationID(r.Context())
	payload := worker.DatasetRollbackPayload{OperationID: opID, StackID: id, NodeID: stack.PrimaryNodeID, Dataset: stack.ZFSDataset, Name: name, DestroyNewer: body.DestroyNewer}
	if err := worker.EnqueueTask(r.Context(), s.db, "rollback-"+opID, worker.TaskDatasetRollback, id, payload); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
}

// ZFS transaction order distinguishes snapshots created within the same second.
func snapshotNewer(snapshot, target ipc.SnapshotInfoResult) bool {
	if snapshot.CreateTXG != 0 && target.CreateTXG != 0 {
		return snapshot.CreateTXG > target.CreateTXG
	}
	return snapshot.CreatedAt > target.CreatedAt
}
