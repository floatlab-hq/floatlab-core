package control

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func registerStackRoutes(r chi.Router, s *Server) {
	r.Get("/stacks", s.handleListStacks)
	r.Post("/stacks", s.handleCreateStack)
	r.Get("/stacks/{id}", s.handleGetStack)
	r.Put("/stacks/{id}/compose", s.handleUpdateStackCompose)
	r.Post("/stacks/{id}/start", s.handleStartStack)
	r.Post("/stacks/{id}/stop", s.handleStopStack)
	r.Delete("/stacks/{id}", s.handleDeleteStack)
	r.Get("/stacks/{id}/state", s.handleGetStackState)
	r.Get("/stacks/{id}/containers", s.handleGetStackContainers)
}

func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.store.ListStacks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Enrich with FSM state.
	type stackWithState struct {
		*config.Stack
		FSMState string `json:"fsm_state"`
	}
	result := make([]stackWithState, 0, len(stacks))
	for _, st := range stacks {
		var state string
		if inst, ok := s.raft.FSM().State(st.ID); ok {
			state = string(inst.State)
		}
		result = append(result, stackWithState{Stack: st, FSMState: state})
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var state string
	if inst, ok := s.raft.FSM().State(id); ok {
		state = string(inst.State)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"stack":     st,
		"fsm_state": state,
	})
}

type createStackRequest struct {
	Name          string `json:"name"`
	Icon          string `json:"icon,omitempty"`
	PrimaryNodeID string `json:"primary_node_id"`
	BackupNodeID  string `json:"backup_node_id,omitempty"`
	ComposeYAML   string `json:"compose_yaml"`
}

func (s *Server) handleCreateStack(w http.ResponseWriter, r *http.Request) {
	var req createStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.PrimaryNodeID == "" {
		writeError(w, http.StatusBadRequest, "primary_node_id is required")
		return
	}
	if req.ComposeYAML == "" {
		writeError(w, http.StatusBadRequest, "compose_yaml is required")
		return
	}

	parsed, err := compose.Parse(r.Context(), req.ComposeYAML, req.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "compose parse error: "+err.Error())
		return
	}
	if err := compose.Validate(parsed); err != nil {
		writeError(w, http.StatusBadRequest, "compose validation error: "+err.Error())
		return
	}

	pool := parsed.Extension.Storage.Pool
	dataset := compose.DatasetPath(pool, req.Name)

	st := &config.Stack{
		ID:            uuid.New().String(),
		Name:          req.Name,
		Icon:          req.Icon,
		PrimaryNodeID: req.PrimaryNodeID,
		BackupNodeID:  req.BackupNodeID,
		ComposeYAML:   req.ComposeYAML,
		ZFSDataset:    dataset,
	}

	if err := s.store.CreateStack(r.Context(), st); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Kick off provisioning via Raft.
	entry := run.StackStateChanged{
		StackID:   st.ID,
		From:      run.StateIdle,
		To:        run.StateProvisioning,
		Event:     run.EventCreateStack,
		Timestamp: time.Now().UTC(),
		NodeID:    req.PrimaryNodeID,
	}
	if err := s.raft.Apply(entry, 10*time.Second); err != nil {
		writeError(w, http.StatusInternalServerError, "raft apply failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, st)
}

func (s *Server) handleUpdateStackCompose(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var body struct {
		ComposeYAML string `json:"compose_yaml"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.ComposeYAML == "" {
		writeError(w, http.StatusBadRequest, "compose_yaml is required")
		return
	}

	parsed, err := compose.Parse(r.Context(), body.ComposeYAML, st.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "compose parse error: "+err.Error())
		return
	}
	if err := compose.Validate(parsed); err != nil {
		writeError(w, http.StatusBadRequest, "compose validation error: "+err.Error())
		return
	}

	if err := s.store.UpdateStackCompose(r.Context(), id, body.ComposeYAML); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleStartStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	inst, ok := s.raft.FSM().State(id)
	if !ok {
		inst = &run.StackInstance{ID: id, State: run.StateIdle}
	}

	updated, err := s.sm.Apply(r.Context(), inst, run.EventStartStack)
	if err != nil {
		if errors.Is(err, run.ErrInvalidTransition) || errors.Is(err, run.ErrTransitionInProgress) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	entry := run.StackStateChanged{
		StackID:   id,
		From:      inst.State,
		To:        updated.State,
		Event:     run.EventStartStack,
		Timestamp: time.Now().UTC(),
		NodeID:    st.PrimaryNodeID,
	}
	if err := s.raft.Apply(entry, 10*time.Second); err != nil {
		writeError(w, http.StatusInternalServerError, "raft apply failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"state": string(updated.State)})
}

func (s *Server) handleStopStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	inst, ok := s.raft.FSM().State(id)
	if !ok {
		writeError(w, http.StatusConflict, "stack has no FSM state")
		return
	}

	updated, err := s.sm.Apply(r.Context(), inst, run.EventStopStack)
	if err != nil {
		if errors.Is(err, run.ErrInvalidTransition) || errors.Is(err, run.ErrTransitionInProgress) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	nodeID := st.PrimaryNodeID
	if inst.State == run.StateRunningBackup && st.BackupNodeID != "" {
		nodeID = st.BackupNodeID
	}

	entry := run.StackStateChanged{
		StackID:   id,
		From:      inst.State,
		To:        updated.State,
		Event:     run.EventStopStack,
		Timestamp: time.Now().UTC(),
		NodeID:    nodeID,
	}
	if err := s.raft.Apply(entry, 10*time.Second); err != nil {
		writeError(w, http.StatusInternalServerError, "raft apply failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]string{"state": string(updated.State)})
}

func (s *Server) handleDeleteStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetStack(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if inst, ok := s.raft.FSM().State(id); ok {
		switch inst.State {
		case run.StateIdle, run.StateFailed:
			// allowed
		default:
			writeError(w, http.StatusConflict,
				"stack must be Idle or Failed to delete; current state: "+string(inst.State))
			return
		}
	}

	if err := s.store.DeleteStack(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetStackState(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	inst, ok := s.raft.FSM().State(id)
	if !ok {
		writeError(w, http.StatusNotFound, "no FSM state for stack "+id)
		return
	}
	writeJSON(w, http.StatusOK, inst)
}

func (s *Server) handleGetStackContainers(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	nodeID := st.PrimaryNodeID
	if inst, ok := s.raft.FSM().State(id); ok && inst.State == run.StateRunningBackup {
		nodeID = st.BackupNodeID
	}

	raw, err := s.hosts.Execute(r.Context(), nodeID, "docker.list", ipc.DockerListPayload{StackID: id})
	if err != nil {
		writeError(w, http.StatusBadGateway, "hostd error: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, raw)
}
