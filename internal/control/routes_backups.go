package control

import (
	"encoding/json"
	"errors"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (s *Server) handleBackupTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.BackupTargets(r.Context(), chi.URLParam(r, "stack_id"))
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, targets)
}
func (s *Server) handlePutBackupTarget(w http.ResponseWriter, r *http.Request) {
	var target config.BackupTarget
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	target.StackID = chi.URLParam(r, "stack_id")
	st, err := s.store.GetStack(r.Context(), target.StackID)
	if err != nil {
		writeError(w, 404, err.Error())
		return
	}
	spec, err := compose.ParseLifecycle(st.ComposeYAML, st.Name)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	mount, ok := spec.Mounts[target.Dataset]
	if !ok {
		writeError(w, 400, "backup dataset must be a managed workload mount")
		return
	}
	if target.NodeID == st.PrimaryNodeID {
		writeError(w, 400, "backup destination cannot be the source node")
		return
	}
	if _, err := s.store.GetNode(r.Context(), target.NodeID); err != nil {
		writeError(w, 400, "unknown backup node")
		return
	}
	tiers, err := store.ParseSnapshotPolicy(target.Schedule)
	if err != nil || len(tiers) == 0 {
		writeError(w, 400, "invalid backup policy")
		return
	}
	target.Origin = "api"
	target.SourceNodeID = st.PrimaryNodeID
	target.LocalPolicy = config.MountPolicy(mount)
	if err := s.store.PutBackupTarget(r.Context(), target); err != nil {
		if errors.Is(err, config.ErrBackupTargetConflict) {
			writeError(w, 409, err.Error())
			return
		}
		writeError(w, 500, err.Error())
		return
	}
	targets, err := s.store.BackupTargets(r.Context(), target.StackID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, saved := range targets {
		if saved.Dataset == target.Dataset && saved.NodeID == target.NodeID {
			writeJSON(w, 200, saved)
			return
		}
	}
	writeError(w, 409, "backup node has been removed")
}
func (s *Server) handleRemoveBackupTarget(w http.ResponseWriter, r *http.Request) {
	var target config.BackupTarget
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil || target.Dataset == "" || target.NodeID == "" {
		writeError(w, 400, "dataset and node_id required")
		return
	}
	if err := s.store.RemoveBackupTarget(r.Context(), chi.URLParam(r, "stack_id"), target.Dataset, target.NodeID); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "cleanup_pending"})
}
