package control

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func registerFailoverRoutes(r chi.Router, s *Server) {
	r.Get("/failover/status", s.handleFailoverStatus)
	r.Post("/failover/{stack_id}/trigger", s.handleTriggerFailover)
	r.Post("/failover/{stack_id}/abort", s.handleAbortFailover)
	r.Get("/failover/{stack_id}/log", s.handleFailoverLog)
}

func (s *Server) handleFailoverStatus(w http.ResponseWriter, r *http.Request) {
	states := s.raft.FSM().AllStates()
	type entry struct {
		StackID string `json:"stack_id"`
		State   string `json:"state"`
	}
	out := make([]entry, 0, len(states))
	for id, inst := range states {
		out = append(out, entry{StackID: id, State: string(inst.State)})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTriggerFailover(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")

	go func() {
		if err := s.seq.Execute(r.Context(), stackID); err != nil {
			s.log.Error("failover: manual trigger", zap.String("stack", stackID), zap.Error(err))
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":   "triggered",
		"stack_id": stackID,
	})
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
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "current_state",
		"state":    string(inst.State),
		"stack_id": stackID,
	})
}

func (s *Server) handleFailoverLog(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT id, kind, severity, title, body, state, created_at
		      FROM notifications WHERE stack_id = ? AND kind = 'failover'
		      ORDER BY created_at DESC LIMIT 50`,
		Params: []interface{}{stackID},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type logEntry struct {
		ID        string `json:"id"`
		Kind      string `json:"kind"`
		Severity  string `json:"severity"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		State     string `json:"state"`
		CreatedAt string `json:"created_at"`
	}
	rows := make([]logEntry, 0, len(result.Values))
	for _, row := range result.Values {
		e := logEntry{}
		e.ID, _ = row[0].(string)
		e.Kind, _ = row[1].(string)
		e.Severity, _ = row[2].(string)
		e.Title, _ = row[3].(string)
		e.Body, _ = row[4].(string)
		e.State, _ = row[5].(string)
		e.CreatedAt, _ = row[6].(string)
		rows = append(rows, e)
	}
	writeJSON(w, http.StatusOK, rows)
}

// handleStackFailover is mounted on /stacks/{id}/failover to match the frontend
// API client which calls POST /stacks/{id}/failover.
func (s *Server) handleStackFailover(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"` // "trigger" | "restore"
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body.Action = "trigger"
	}
	stackID := chi.URLParam(r, "id")

	go func() {
		var err error
		if body.Action == "restore" {
			err = s.seq.Restore(r.Context(), stackID)
		} else {
			err = s.seq.Execute(r.Context(), stackID)
		}
		if err != nil {
			s.log.Error("failover: stack action",
				zap.String("stack", stackID),
				zap.String("action", body.Action),
				zap.Error(err))
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":   "triggered",
		"action":   body.Action,
		"stack_id": stackID,
	})
}

// handleStackRestore is mounted on /stacks/{id}/restore — matches frontend restoreStack().
func (s *Server) handleStackRestore(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "id")
	go func() {
		if err := s.seq.Restore(r.Context(), stackID); err != nil {
			s.log.Error("failover: restore", zap.String("stack", stackID), zap.Error(err))
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":   "triggered",
		"action":   "restore",
		"stack_id": stackID,
	})
}

