package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.ops.Get(r.Context(), chi.URLParam(r, "operationId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, op)
}

func (s *Server) handleUpgradeStack(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), stackID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	instance, ok := s.raft.FSM().State(stackID)
	if !ok || instance.State != run.StateRunningPrimary {
		writeError(w, http.StatusConflict, "stack must be running on its primary node")
		return
	}
	var request struct {
		Images map[string]string `json:"images"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	updatedCompose, err := compose.UpdateImages(stack.ComposeYAML, request.Images)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	spec, err := compose.ParseLifecycle(updatedCompose, stack.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.sm.Apply(r.Context(), instance, run.EventUpdateStack)
	if err != nil {
		if errors.Is(err, run.ErrInvalidTransition) || errors.Is(err, run.ErrTransitionInProgress) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.raft.Apply(run.StackStateChanged{StackID: stackID, From: instance.State, To: updated.State, Event: run.EventUpdateStack, Timestamp: time.Now().UTC(), NodeID: stack.PrimaryNodeID}, 10*time.Second); err != nil {
		writeError(w, http.StatusInternalServerError, "raft apply failed: "+err.Error())
		return
	}
	opID := operationID(r.Context())
	if err := worker.EnqueueTask(r.Context(), s.db, "upgrade-"+opID, worker.TaskStackUpgrade, stackID, worker.StackUpgradePayload{
		OperationID: opID, StackID: stackID, NodeID: stack.PrimaryNodeID, DatasetPath: stack.ZFSDataset,
		OldCompose: stack.ComposeYAML, NewCompose: updatedCompose, Services: mapKeys(request.Images),
		HealthTimeout: spec.HealthTimeout.String(), Actor: actor(r),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "enqueue upgrade: "+err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"stack_id": stackID, "state": string(updated.State)})
}

func (s *Server) handleRestartStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	instance, ok := s.raft.FSM().State(id)
	if !ok || (instance.State != run.StateRunningPrimary && instance.State != run.StateRunningBackup) {
		writeError(w, http.StatusConflict, "stack must be running")
		return
	}
	nodeID := stack.PrimaryNodeID
	if instance.State == run.StateRunningBackup {
		nodeID = stack.BackupNodeID
	}
	spec, err := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, err := s.runtimeCompose(r.Context(), stack.ID, stack.Name, stack.ComposeYAML)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	opID := operationID(r.Context())
	payload := worker.StackRestartPayload{OperationID: opID, StackID: id, NodeID: nodeID, DatasetPath: stack.ZFSDataset, ComposeFile: runtime, HealthTimeout: spec.HealthTimeout.String(), Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "restart-"+opID, worker.TaskStackRestart, id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
}

func (s *Server) handleRestoreSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var request struct {
		SnapshotID string `json:"snapshot_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.SnapshotID == "" {
		writeError(w, http.StatusBadRequest, "snapshot_id is required")
		return
	}
	recovery, err := s.db.Query(r.Context(), rqlite.Statement{SQL: `SELECT compose_yaml FROM recovery_points WHERE stack_id=? AND snapshot_id=? AND deleted_at IS NULL`, Params: []interface{}{id, request.SnapshotID}})
	if err != nil || len(recovery.Values) == 0 {
		writeError(w, http.StatusNotFound, "snapshot recovery metadata not found")
		return
	}
	restoredCompose, _ := recovery.Values[0][0].(string)
	instance, ok := s.raft.FSM().State(id)
	if !ok {
		writeError(w, http.StatusConflict, "stack has no lifecycle state")
		return
	}
	wasRunning := instance.State == run.StateRunningPrimary || instance.State == run.StateRunningBackup
	nodeID := stack.PrimaryNodeID
	if instance.State == run.StateRunningBackup {
		nodeID = stack.BackupNodeID
	}
	spec, err := compose.ParseLifecycle(restoredCompose, stack.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runtime, err := s.runtimeCompose(r.Context(), stack.ID, stack.Name, restoredCompose)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	opID := operationID(r.Context())
	payload := worker.StackRestorePayload{OperationID: opID, StackID: id, NodeID: nodeID, DatasetPath: stack.ZFSDataset, Snapshot: request.SnapshotID, ComposeFile: runtime, SourceCompose: restoredCompose, HealthTimeout: spec.HealthTimeout.String(), WasRunning: wasRunning, Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "restore-"+opID, worker.TaskStackRestore, id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
}

func (s *Server) runtimeCompose(ctx context.Context, stackID, name, source string) (string, error) {
	stackIP := ""
	result, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT stack_ip FROM stack_runtime WHERE stack_id=?`, Params: []interface{}{stackID}})
	if err == nil && len(result.Values) > 0 {
		stackIP, _ = result.Values[0][0].(string)
	}
	return compose.RuntimeYAML(source, name, stackIP)
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
