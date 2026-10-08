package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
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
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Post("/stacks/validate", s.handleValidateStack)
	})
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Use(s.idempotency)
		r.Get("/stacks", s.handleListStacks)
		r.Post("/stacks", s.handleCreateStack)
		r.Get("/stacks/{id}", s.handleGetStack)
		r.Put("/stacks/{id}/compose", s.handleUpdateStackCompose)
		r.Post("/stacks/{id}/start", s.handleStartStack)
		r.Post("/stacks/{id}/stop", s.handleStopStack)
		r.Post("/stacks/{id}/upgrade", s.handleUpgradeStack)
		r.Post("/stacks/{id}/restart", s.handleRestartStack)
		r.Post("/stacks/{id}/failover", s.handleStackFailover)
		r.Post("/stacks/{id}/restore", s.handleRestoreSnapshot)
		r.Delete("/stacks/{id}", s.handleDeleteStack)
		r.Get("/stacks/{id}/state", s.handleGetStackState)
		r.Get("/stacks/{id}/containers", s.handleGetStackContainers)
		r.Post("/stacks/{id}/containers/{containerId}/start", s.handleStartStackContainer)
		r.Post("/stacks/{id}/containers/{containerId}/stop", s.handleStopStackContainer)
		r.Get("/stacks/{id}/status", s.handleStackStatus)
		r.Get("/stacks/{id}/config", s.handleStackConfig)
		r.Get("/stacks/{id}/snapshots", s.handleStackSnapshots)
		r.Post("/stacks/{id}/snapshots", s.handleCreateStackSnapshot)
		r.Delete("/stacks/{id}/snapshots/{snapshotId}", s.handleDeleteStackSnapshot)
		r.Get("/stacks/{id}/alerts", s.handleStackAlerts)
		r.Get("/stacks/{id}/events", s.handleStackEvents)
		r.Get("/stacks/{id}/containers/{containerId}/terminal", s.handleStackTerminal)
		r.Post("/stacks/{id}/containers/{containerId}/exec", s.handleContainerExec)
		r.Get("/operations/{operationId}", s.handleGetOperation)
		r.Post("/internal/alerts/transition", s.handleAlertTransition)
	})
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
	Name          string  `json:"name"`
	Icon          string  `json:"icon,omitempty"`
	PrimaryNodeID *string `json:"primary_node,omitempty"`
	BackupNodeID  *string `json:"secondary_node,omitempty"`
	ComposeFile   string  `json:"compose_file"`
	FailoverMode  *string `json:"failover_mode,omitempty"`
	AutoTrigger   *string `json:"auto_trigger_after,omitempty"`
}

type validateStackRequest struct {
	Name        string `json:"name"`
	ComposeFile string `json:"compose_file"`
	StackID     string `json:"stack_id"`
}

func validateCompose(r *http.Request, source, name string) (string, *compose.ParsedStack, error) {
	canonical, err := compose.CanonicalSource(source, name)
	if err != nil {
		return "", nil, err
	}
	parsed, err := compose.ParseAndValidate(r.Context(), canonical, name)
	if err != nil {
		return "", nil, err
	}
	if _, err := compose.ParseLifecycle(canonical, name); err != nil {
		return "", nil, err
	}
	return canonical, parsed, nil
}

func writeComposeValidationError(w http.ResponseWriter, err error) {
	writeErrorCode(w, http.StatusBadRequest, "compose_validation", "compose validation error: "+err.Error())
}

func (s *Server) handleValidateStack(w http.ResponseWriter, r *http.Request) {
	var req validateStackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "compose_validation", "invalid JSON: "+err.Error())
		return
	}
	if req.ComposeFile == "" {
		writeErrorCode(w, http.StatusBadRequest, "compose_validation", "compose_file is required")
		return
	}
	if req.StackID != "" {
		st, err := s.store.GetStack(r.Context(), req.StackID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		req.Name = st.Name
		canonical, parsed, err := validateCompose(r, req.ComposeFile, req.Name)
		if err != nil {
			writeComposeValidationError(w, err)
			return
		}
		_ = canonical
		if parsed.Extension.PrimaryNode != st.PrimaryNodeID || parsed.Extension.SecondaryNode != st.BackupNodeID {
			writeErrorCode(w, http.StatusBadRequest, "compose_validation", "compose updates cannot change node assignments")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if req.Name == "" {
		writeErrorCode(w, http.StatusBadRequest, "compose_validation", "name is required")
		return
	}
	name, err := compose.Slug(req.Name)
	if err != nil {
		writeComposeValidationError(w, err)
		return
	}
	if _, _, err := validateCompose(r, req.ComposeFile, name); err != nil {
		writeComposeValidationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	if req.ComposeFile == "" {
		writeError(w, http.StatusBadRequest, "compose_file is required")
		return
	}
	name, err := compose.Slug(req.Name)
	if err != nil {
		writeComposeValidationError(w, err)
		return
	}
	canonical, parsed, err := validateCompose(r, req.ComposeFile, name)
	if err != nil {
		writeComposeValidationError(w, err)
		return
	}
	if req.PrimaryNodeID != nil && parsed.Extension.PrimaryNode != *req.PrimaryNodeID {
		writeError(w, http.StatusBadRequest, "compose node assignments must match the request")
		return
	}
	if req.BackupNodeID != nil && parsed.Extension.SecondaryNode != *req.BackupNodeID {
		writeError(w, http.StatusBadRequest, "compose node assignments must match the request")
		return
	}
	failoverMode := parsed.Extension.Failover.Mode
	if failoverMode == "" {
		failoverMode = "manual"
	}
	if req.FailoverMode != nil && failoverMode != *req.FailoverMode {
		writeError(w, http.StatusBadRequest, "compose failover settings must match the request")
		return
	}
	autoTrigger := parsed.Extension.Failover.AutoTriggerAfter
	if autoTrigger == "" {
		autoTrigger = "120s"
	}
	if req.AutoTrigger != nil && autoTrigger != *req.AutoTrigger {
		writeError(w, http.StatusBadRequest, "compose failover settings must match the request")
		return
	}
	if duration, err := time.ParseDuration(autoTrigger); err != nil || duration <= 0 {
		writeComposeValidationError(w, errors.New("auto_trigger_after must be a positive duration"))
		return
	}

	pool := parsed.Extension.Storage.Pool
	dataset := compose.DatasetPath(pool, name)

	st := &config.Stack{
		ID:               uuid.New().String(),
		Name:             name,
		Icon:             req.Icon,
		PrimaryNodeID:    parsed.Extension.PrimaryNode,
		BackupNodeID:     parsed.Extension.SecondaryNode,
		ComposeYAML:      canonical,
		ZFSDataset:       dataset,
		FailoverMode:     failoverMode,
		AutoTriggerAfter: autoTrigger,
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
		NodeID:    st.PrimaryNodeID,
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

	canonical, parsed, err := validateCompose(r, body.ComposeFile, st.Name)
	if err != nil {
		writeComposeValidationError(w, err)
		return
	}
	if parsed.Extension.PrimaryNode != st.PrimaryNodeID || parsed.Extension.SecondaryNode != st.BackupNodeID {
		writeError(w, http.StatusBadRequest, "compose updates cannot change node assignments")
		return
	}

	if err := s.store.UpdateStackCompose(r.Context(), id, canonical); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st.ComposeYAML = canonical
	st.UpdatedAt = time.Now().UTC()
	var state string
	if inst, ok := s.raft.FSM().State(id); ok {
		state = string(inst.State)
	}
	writeJSON(w, http.StatusOK, toStackResponse(st, state))
}

func (s *Server) handleStartStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		s.handleCreateAndStartStack(w, r, id)
		return
	}
	if r.ContentLength != 0 {
		var source, staging string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-tar") {
			staging, err = os.MkdirTemp("/floatlab", ".staging-"+st.Name+"-")
			if err == nil {
				defer os.RemoveAll(staging)
				err = compose.ExtractProject(r.Body, staging)
			}
			if err == nil {
				var data []byte
				data, err = os.ReadFile(filepath.Join(staging, "docker-compose.yml"))
				source = string(data)
			}
		} else {
			var data []byte
			data, err = io.ReadAll(r.Body)
			source = string(data)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		canonical, err := compose.CanonicalSource(source, st.Name)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		workingDir := filepath.Join("/floatlab", st.Name)
		if staging != "" {
			workingDir = staging
		}
		if _, err := compose.ParseLifecycleAt(canonical, st.Name, workingDir); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if staging != "" {
			if err := compose.CopyProject(staging, filepath.Join("/floatlab", st.Name)); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if err := s.store.UpdateStackCompose(r.Context(), st.ID, canonical); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		st.ComposeYAML = canonical
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

func (s *Server) handleCreateAndStartStack(w http.ResponseWriter, r *http.Request, requestedName string) {
	name, err := compose.Slug(requestedName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var source string
	var staging string
	contentType := strings.Split(r.Header.Get("Content-Type"), ";")[0]
	if contentType == "application/x-tar" {
		if err := os.MkdirAll("/floatlab", 0755); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		staging, err = os.MkdirTemp("/floatlab", ".staging-"+name+"-")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer os.RemoveAll(staging)
		if err := compose.ExtractProject(r.Body, staging); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		data, err := os.ReadFile(filepath.Join(staging, "docker-compose.yml"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "archive must contain docker-compose.yml")
			return
		}
		source = string(data)
	} else {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		source = string(data)
	}
	if source == "" {
		writeError(w, http.StatusBadRequest, "compose project is required for a new stack")
		return
	}
	source, err = compose.CanonicalSource(source, name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	workingDir := filepath.Join("/floatlab", name)
	if staging != "" {
		workingDir = staging
	}
	if _, err := compose.ParseLifecycleAt(source, name, workingDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil || len(nodes) == 0 {
		writeError(w, http.StatusServiceUnavailable, "no execution node is registered")
		return
	}
	if staging != "" {
		if _, err := s.hosts.Execute(r.Context(), nodes[0].ID, "fs.dataset.create", ipc.DatasetCreatePayload{Dataset: "floatlab/" + name, BlockSize: "32K", Compression: "lz4"}); err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := compose.CopyProject(staging, filepath.Join("/floatlab", name)); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	stack := &config.Stack{ID: uuid.NewString(), Name: name, PrimaryNodeID: nodes[0].ID, ComposeYAML: source, ZFSDataset: "floatlab/" + name, FailoverMode: "manual"}
	if err := s.store.CreateStack(r.Context(), stack); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	_ = s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `UPDATE operations SET stack_id=? WHERE id=?`, Params: []interface{}{stack.ID, operationID(r.Context())}}})
	if err := s.raft.Apply(run.StackStateChanged{StackID: stack.ID, From: run.StateIdle, To: run.StateProvisioning, Event: run.EventCreateStack, Timestamp: time.Now().UTC(), NodeID: stack.PrimaryNodeID}, 10*time.Second); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": operationID(r.Context()), "stack_id": stack.ID, "status": "pending"})
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
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	nodeID := stack.PrimaryNodeID
	if instance, ok := s.raft.FSM().State(id); ok && instance.State == run.StateRunningBackup {
		nodeID = stack.BackupNodeID
	}
	purge, _ := strconv.ParseBool(r.URL.Query().Get("purge"))
	opID := operationID(r.Context())
	payload := worker.StackDeletePayload{OperationID: opID, StackID: id, NodeID: nodeID, DatasetPath: stack.ZFSDataset, Purge: purge, Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "delete-"+opID, worker.TaskStackDelete, id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
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

	nodeID := stackNode(st, s.raft.FSM().State)

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
	containers := containerResponses(result.Containers, id, nodeID)
	writeJSON(w, http.StatusOK, containers)
}

func stackNode(st *config.Stack, state func(string) (*run.StackInstance, bool)) string {
	if inst, ok := state(st.ID); ok && inst.State == run.StateRunningBackup && st.BackupNodeID != "" {
		return st.BackupNodeID
	}
	return st.PrimaryNodeID
}

func containerResponses(containers []ipc.ContainerInfo, stackID, nodeID string) []containerResponse {
	result := make([]containerResponse, 0, len(containers))
	for _, c := range containers {
		result = append(result, containerResponse{ID: c.ID, Name: c.Name, Image: c.Image, Status: containerStatus(c.State), Health: c.Health, NodeID: nodeID, StackID: stackID, Service: c.Service})
	}
	return result
}

func containerStatus(state string) string {
	switch state {
	case "running", "paused", "starting", "error":
		return state
	case "created", "exited", "dead", "removing":
		return "stopped"
	default:
		return "error"
	}
}

func (s *Server) handleStartStackContainer(w http.ResponseWriter, r *http.Request) {
	s.handleStackContainerLifecycle(w, r, "docker.start")
}

func (s *Server) handleStopStackContainer(w http.ResponseWriter, r *http.Request) {
	s.handleStackContainerLifecycle(w, r, "docker.stop")
}

func (s *Server) handleStackContainerLifecycle(w http.ResponseWriter, r *http.Request, action string) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	inst, ok := s.raft.FSM().State(id)
	if !ok || (inst.State != run.StateRunningPrimary && inst.State != run.StateRunningBackup) {
		writeError(w, http.StatusConflict, "stack is not running")
		return
	}
	nodeID := stackNode(st, s.raft.FSM().State)
	containerID := chi.URLParam(r, "containerId")
	raw, err := s.hosts.Execute(r.Context(), nodeID, "docker.list", ipc.DockerListPayload{StackID: id})
	if err != nil {
		writeError(w, http.StatusBadGateway, "hostd error: "+err.Error())
		return
	}
	var listed ipc.DockerListResult
	if err := json.Unmarshal(raw, &listed); err != nil {
		writeError(w, http.StatusInternalServerError, "parse hostd response: "+err.Error())
		return
	}
	var container *ipc.ContainerInfo
	for i := range listed.Containers {
		candidate := &listed.Containers[i]
		if candidate.ID == containerID || candidate.Name == containerID {
			container = candidate
			break
		}
	}
	if container == nil {
		writeError(w, http.StatusNotFound, "container does not belong to stack")
		return
	}
	raw, err = s.hosts.Execute(r.Context(), nodeID, action, ipc.DockerContainerPayload{StackID: id, ContainerID: container.ID})
	if err != nil {
		writeError(w, http.StatusBadGateway, "hostd error: "+err.Error())
		return
	}
	var result ipc.DockerContainerResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, "parse hostd response: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, containerResponses([]ipc.ContainerInfo{result.Container}, id, nodeID)[0])
}
