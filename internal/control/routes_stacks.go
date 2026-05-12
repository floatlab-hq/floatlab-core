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

// stackResponse matches the frontend Stack interface field names exactly.
type stackResponse struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	State            string    `json:"state"`
	PrimaryNode      string    `json:"primary_node"`
	SecondaryNode    string    `json:"secondary_node"`
	ComposeFile      string    `json:"compose_file"`
	DatasetPath      string    `json:"dataset_path"`
	FailoverMode     string    `json:"failover_mode"`
	AutoTriggerAfter string    `json:"auto_trigger_after"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toStackResponse(st *config.Stack, fsmState string) stackResponse {
	return stackResponse{
		ID:               st.ID,
		Name:             st.Name,
		State:            fsmState,
		PrimaryNode:      st.PrimaryNodeID,
		SecondaryNode:    st.BackupNodeID,
		ComposeFile:      st.ComposeYAML,
		DatasetPath:      st.ZFSDataset,
		FailoverMode:     st.FailoverMode,
		AutoTriggerAfter: st.AutoTriggerAfter,
		CreatedAt:        st.CreatedAt,
		UpdatedAt:        st.UpdatedAt,
	}
}

// containerResponse matches the frontend Container interface.
type containerResponse struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	Status  string `json:"status"`
	Health  string `json:"health"`
	NodeID  string `json:"node_id"`
	StackID string `json:"stack_id"`
	Service string `json:"service"`
}

func registerStackRoutes(r chi.Router, s *Server) {
	r.Get("/stacks", s.handleListStacks)
	r.Post("/stacks", s.handleCreateStack)
	r.Get("/stacks/{id}", s.handleGetStack)
	r.Put("/stacks/{id}/compose", s.handleUpdateStackCompose)
	r.Post("/stacks/{id}/start", s.handleStartStack)
	r.Post("/stacks/{id}/stop", s.handleStopStack)
	r.Post("/stacks/{id}/failover", s.handleStackFailover)
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
	result := make([]stackResponse, 0, len(stacks))
	for _, st := range stacks {
		var state string
		if inst, ok := s.raft.FSM().State(st.ID); ok {
			state = string(inst.State)
		}
		result = append(result, toStackResponse(st, state))
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
	writeJSON(w, http.StatusOK, toStackResponse(st, state))
}

type createStackRequest struct {
	Name          string `json:"name"`
	Icon          string `json:"icon,omitempty"`
	PrimaryNodeID string `json:"primary_node"`
	BackupNodeID  string `json:"secondary_node,omitempty"`
	ComposeFile   string `json:"compose_file"`
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
		writeError(w, http.StatusBadRequest, "primary_node is required")
		return
	}
	if req.ComposeFile == "" {
		writeError(w, http.StatusBadRequest, "compose_file is required")
		return
	}

	parsed, err := compose.Parse(r.Context(), req.ComposeFile, req.Name)
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
		ComposeYAML:   req.ComposeFile,
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

	writeJSON(w, http.StatusCreated, toStackResponse(st, string(run.StateProvisioning)))
}

func (s *Server) handleUpdateStackCompose(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	var body struct {
		ComposeFile string `json:"compose_file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.ComposeFile == "" {
		writeError(w, http.StatusBadRequest, "compose_file is required")
		return
	}

	parsed, err := compose.Parse(r.Context(), body.ComposeFile, st.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, "compose parse error: "+err.Error())
		return
	}
	if err := compose.Validate(parsed); err != nil {
		writeError(w, http.StatusBadRequest, "compose validation error: "+err.Error())
		return
	}

	if err := s.store.UpdateStackCompose(r.Context(), id, body.ComposeFile); err != nil {
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
	var result ipc.DockerListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, "parse hostd response: "+err.Error())
		return
	}
	containers := make([]containerResponse, 0, len(result.Containers))
	for _, c := range result.Containers {
		containers = append(containers, containerResponse{
			ID:      c.ID,
			Name:    c.Name,
			Image:   c.Image,
			Status:  c.State,
			Health:  c.Health,
			NodeID:  nodeID,
			StackID: id,
			Service: c.Service,
		})
	}
	writeJSON(w, http.StatusOK, containers)
}
