package control

import (
	"context"
	"encoding/json"
	"github.com/floatlab/floatlab-core/pkg/run"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"
)

func registerFailoverRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Use(s.idempotency)
		r.Get("/failover/status", s.handleFailoverStatus)
		r.Post("/failover/{stack_id}/trigger", s.handleTriggerFailover)
		r.Post("/failover/{stack_id}/abort", s.handleAbortFailover)
		r.Get("/failover/{stack_id}/log", s.handleFailoverLog)
	})
}

func (s *Server) handleFailoverStatus(w http.ResponseWriter, r *http.Request) {
	states := s.raft.FSM().AllStates()
	type entry struct {
		StackID        string  `json:"stack_id"`
		State          string  `json:"state"`
		TriggeredBy    string  `json:"triggered_by"`
		StartedAt      *string `json:"started_at"`
		LifecycleState string  `json:"lifecycle_state"`
	}
	out := make([]entry, 0, len(states))
	for id, inst := range states {
		state := "idle"
		if inst.State == run.StateFailingOver || inst.State == run.StateRestoring {
			state = "running"
		}
		if inst.State == run.StateFailed {
			state = "failed"
		}
		if inst.State == run.StateRunningBackup {
			state = "complete"
		}
		out = append(out, entry{StackID: id, State: state, TriggeredBy: "manual", LifecycleState: string(inst.State)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTriggerFailover(w http.ResponseWriter, r *http.Request) {
	s.submitFailover(w, r, chi.URLParam(r, "stack_id"), false)
}

func (s *Server) handleAbortFailover(w http.ResponseWriter, r *http.Request) {
	// Abort is only meaningful during an active sequence. For now we surface the
	// current state so the caller knows whether abort had any effect.
	stackID := chi.URLParam(r, "stack_id")
	inst, ok := s.raft.FSM().State(stackID)
	if !ok {
		writeError(w, http.StatusNotFound, "stack not found in FSM")
		return
	}
	if inst.State != run.StateFailingOver {
		writeError(w, 409, "no active failover")
		return
	}
	if !s.seq.Abort(stackID) {
		writeError(w, 409, "no cancellable failover")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":   "aborted",
		"state":    string(inst.State),
		"stack_id": stackID,
	})
}

func (s *Server) handleFailoverLog(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	rows, err := s.queryObjects(r, `SELECT step,name,state,started_at,completed_at,detail FROM failover_steps WHERE stack_id=? AND sequence_id=(SELECT sequence_id FROM failover_steps WHERE stack_id=? ORDER BY started_at DESC LIMIT 1) ORDER BY step`, stackID, stackID)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(rows) == 0 {
		writeError(w, 404, "no failover history")
		return
	}
	writeJSON(w, 200, rows)
}

// handleStackFailover is mounted on /stacks/{id}/failover to match the frontend
// API client which calls POST /stacks/{id}/failover.
func (s *Server) handleStackFailover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err != io.EOF {
		writeError(w, 400, "invalid JSON")
		return
	}
	if body.Action != "" && body.Action != "trigger" && body.Action != "restore" {
		writeError(w, 400, "action must be trigger or restore")
		return
	}
	s.submitFailover(w, r, chi.URLParam(r, "id"), body.Action == "restore")
}

func (s *Server) handleStackRestore(w http.ResponseWriter, r *http.Request) {
	s.submitFailover(w, r, chi.URLParam(r, "id"), true)
}

func (s *Server) submitFailover(w http.ResponseWriter, r *http.Request, id string, restore bool) {
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, 404, "stack not found")
		return
	}
	if stack.BackupNodeID == "" || stack.BackupNodeID == stack.PrimaryNodeID {
		writeError(w, 409, "a distinct secondary node is required")
		return
	}
	instance, ok := s.raft.FSM().State(id)
	expected := run.StateRunningPrimary
	if restore {
		expected = run.StateRunningBackup
	}
	if !ok || instance.State != expected {
		writeError(w, 409, "stack is not in the required lifecycle state")
		return
	}
	if _, err := s.store.GetNode(r.Context(), stack.BackupNodeID); err != nil {
		writeError(w, 409, "secondary node not found")
		return
	}
	opID := operationID(r.Context())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		var err error
		if restore {
			err = s.seq.Restore(ctx, id)
		} else {
			err = s.seq.Execute(ctx, id)
		}
		state, message := "succeeded", ""
		if err != nil {
			state = "failed"
			message = err.Error()
			s.log.Error("failover", zap.String("stack", id), zap.Error(err))
		}
		_ = s.ops.Update(context.Background(), opID, state, state, message)
	}()
	writeJSON(w, 202, map[string]interface{}{"operation_id": opID, "stack_id": id, "status": "triggered", "state": "running", "triggered_by": "manual", "started_at": time.Now().UTC().Format(time.RFC3339)})
}
