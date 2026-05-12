package control

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	hraft "github.com/hashicorp/raft"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func registerHealthRoutes(r chi.Router, s *Server) {
	r.Get("/health", s.handleHealth)
	r.Get("/health/ready", s.handleHealthReady)
	r.Get("/health/raft", s.handleHealthRaft)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "ok",
		"raft_state": s.raft.State().String(),
		"leader":     s.raft.Leader(),
	})
}

func (s *Server) handleHealthReady(w http.ResponseWriter, r *http.Request) {
	state := s.raft.State()
	if state != hraft.Leader && state != hraft.Follower {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not_ready",
			"reason": "raft not stable: " + state.String(),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleHealthRaft(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.raft.Stats())
}

func registerNodeRoutes(r chi.Router, s *Server) {
	r.Get("/nodes", s.handleListNodes)
	r.Post("/nodes", s.handleCreateNode)
	r.Get("/nodes/{id}", s.handleGetNode)
	r.Put("/nodes/{id}", s.handleUpdateNode)
	r.Delete("/nodes/{id}", s.handleDeleteNode)
	r.Get("/nodes/{id}/health", s.handleNodeHealth)
	r.Get("/nodes/{id}/stacks", s.handleNodeStacks)
}

func (s *Server) handleListNodes(w http.ResponseWriter, r *http.Request) {
	nodes, err := s.store.ListNodes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, nodes)
}

func (s *Server) handleCreateNode(w http.ResponseWriter, r *http.Request) {
	var n config.Node
	if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := s.store.CreateNode(r.Context(), &n); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, n)
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	n, err := s.store.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetNode(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body config.Node
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	body.ID = existing.ID
	body.CreatedAt = existing.CreatedAt
	if err := s.store.UpdateNode(r.Context(), &body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteNode(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodeStacks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	all, err := s.store.ListStacks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var result []*config.Stack
	for _, st := range all {
		if st.PrimaryNodeID == id || st.BackupNodeID == id {
			result = append(result, st)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func registerNetworkRoutes(r chi.Router, s *Server) {
	r.Get("/network/pools", stub([]interface{}{}))
	r.Get("/network/allocations", s.handleListAllocations)
	r.Delete("/network/allocations/{id}", s.handleDeleteAllocation)
}
func registerNotifyRoutes(r chi.Router, s *Server) {
	r.Get("/notifications", s.handleListNotifications)
	r.Post("/notifications/{id}/silence", s.handleSilenceNotification)
	r.Post("/notifications/{id}/resolve", s.handleResolveNotification)
	r.Delete("/notifications/{id}", s.handleDeleteNotification)
}
func registerEventRoutes(r chi.Router, s *Server) { r.Get("/events", s.handleEvents) }

func (s *Server) handleListAllocations(w http.ResponseWriter, r *http.Request) {
	stackID := r.URL.Query().Get("stack_id")
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL:    `SELECT id, stack_id, service, address, prefix_pool, allocated_at FROM ip_reservations WHERE (? = '' OR stack_id = ?) ORDER BY allocated_at DESC`,
		Params: []interface{}{stackID, stackID},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type allocation struct {
		ID          string `json:"id"`
		StackID     string `json:"stack_id"`
		Service     string `json:"service"`
		Address     string `json:"address"`
		PrefixPool  string `json:"prefix_pool"`
		AllocatedAt string `json:"allocated_at"`
	}
	rows := make([]allocation, 0, len(result.Values))
	for _, row := range result.Values {
		a := allocation{}
		a.ID, _ = row[0].(string)
		a.StackID, _ = row[1].(string)
		a.Service, _ = row[2].(string)
		a.Address, _ = row[3].(string)
		a.PrefixPool, _ = row[4].(string)
		a.AllocatedAt, _ = row[5].(string)
		rows = append(rows, a)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT id, alert_id, stack_id, node_id, kind, severity, title, body, state, created_at, resolved_at
		      FROM notifications ORDER BY created_at DESC LIMIT 200`,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type notification struct {
		ID         string  `json:"id"`
		AlertID    string  `json:"alert_id"`
		StackID    string  `json:"stack_id,omitempty"`
		NodeID     string  `json:"node_id,omitempty"`
		Kind       string  `json:"kind"`
		Severity   string  `json:"severity"`
		Title      string  `json:"title"`
		Body       string  `json:"body"`
		State      string  `json:"state"`
		CreatedAt  string  `json:"created_at"`
		ResolvedAt *string `json:"resolved_at"`
	}
	rows := make([]notification, 0, len(result.Values))
	for _, row := range result.Values {
		n := notification{}
		n.ID, _ = row[0].(string)
		n.AlertID, _ = row[1].(string)
		n.StackID, _ = row[2].(string)
		n.NodeID, _ = row[3].(string)
		n.Kind, _ = row[4].(string)
		n.Severity, _ = row[5].(string)
		n.Title, _ = row[6].(string)
		n.Body, _ = row[7].(string)
		n.State, _ = row[8].(string)
		n.CreatedAt, _ = row[9].(string)
		if v, ok := row[10].(string); ok && v != "" {
			n.ResolvedAt = &v
		}
		rows = append(rows, n)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleResolveNotification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{
		SQL:    `UPDATE notifications SET state = 'read', resolved_at = datetime('now') WHERE id = ?`,
		Params: []interface{}{id},
	}}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

func (s *Server) handleDeleteAllocation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{
		SQL:    `DELETE FROM ip_reservations WHERE id = ?`,
		Params: []interface{}{id},
	}}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSilenceNotification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		Until string `json:"until"` // RFC3339
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{
		SQL:    `UPDATE notifications SET silenced_until = ? WHERE id = ?`,
		Params: []interface{}{body.Until, id},
	}}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "silenced"})
}

func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{
		SQL:    `DELETE FROM notifications WHERE id = ?`,
		Params: []interface{}{id},
	}}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, err := s.hosts.Execute(r.Context(), id, "sys.info", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "offline", "latency_ms": 0})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "online", "latency_ms": 1})
}

func stub(v interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	_, _ = w.Write([]byte(": keepalive\n\n"))
	flusher.Flush()
	<-r.Context().Done()
}
