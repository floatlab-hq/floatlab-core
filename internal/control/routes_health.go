package control

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	hraft "github.com/hashicorp/raft"

	"github.com/floatlab/floatlab-core/pkg/config"
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

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteNode(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, err := s.hosts.Execute(r.Context(), id, "sys.info", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "offline", "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "online"})
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

// Stub route registrations for domains implemented in Sprint 2+
func registerStackRoutes(r chi.Router, s *Server) {
	r.Get("/stacks", s.handleListStacks)
	r.Get("/stacks/{id}", s.handleGetStack)
}
func registerStorageRoutes(r chi.Router, s *Server)  { r.Get("/storage/pools", stub([]interface{}{})) }
func registerFailoverRoutes(r chi.Router, s *Server) {
	r.Get("/failover/status", stub(map[string]string{"status": "none"}))
}
func registerNetworkRoutes(r chi.Router, s *Server) { r.Get("/network/pools", stub([]interface{}{})) }
func registerLogRoutes(r chi.Router, s *Server)     { r.Get("/logs/search", stub([]interface{}{})) }
func registerStatsRoutes(r chi.Router, s *Server)   { r.Get("/stats/query", stub([]interface{}{})) }
func registerNotifyRoutes(r chi.Router, s *Server)  { r.Get("/notifications", stub([]interface{}{})) }
func registerEventRoutes(r chi.Router, s *Server)   { r.Get("/events", s.handleEvents) }

func stub(v interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) handleListStacks(w http.ResponseWriter, r *http.Request) {
	stacks, err := s.store.ListStacks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stacks)
}

func (s *Server) handleGetStack(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
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
