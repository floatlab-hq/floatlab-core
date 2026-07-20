package control

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/floatlab/floatlab-core/internal/ipam"
)

func (s *Server) handleListNetworkPools(w http.ResponseWriter, r *http.Request) {
	pools, err := ipam.ListPools(r.Context(), s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pools)
}

func (s *Server) handleCreateNetworkPool(w http.ResponseWriter, r *http.Request) {
	var pool ipam.Pool
	if err := json.NewDecoder(r.Body).Decode(&pool); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	pool.ID = ""
	if err := ipam.SavePool(r.Context(), s.db, &pool); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, pool)
}

func (s *Server) handleUpdateNetworkPool(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	pools, err := ipam.ListPools(r.Context(), s.db)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *ipam.Pool
	for i := range pools {
		if pools[i].ID == id {
			existing = &pools[i]
			break
		}
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "network pool not found")
		return
	}
	var pool ipam.Pool
	if err := json.NewDecoder(r.Body).Decode(&pool); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	pool.ID, pool.CreatedAt = id, existing.CreatedAt
	if err := ipam.SavePool(r.Context(), s.db, &pool); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pool)
}

func (s *Server) handleDeleteNetworkPool(w http.ResponseWriter, r *http.Request) {
	if err := ipam.DeletePool(r.Context(), s.db, chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
