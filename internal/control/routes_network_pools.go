package control

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
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
	if err := s.validateLocalPool(r.Context(), pool); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err := ipam.ValidateHosts(r.Context(), s.db, s.hosts, pool); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := s.saveNetworkPool(r.Context(), &pool); err != nil {
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
	if err := s.validateLocalPool(r.Context(), pool); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	if err := ipam.ValidateHosts(r.Context(), s.db, s.hosts, pool); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	for _, node := range existing.NodeIDs {
		if !contains(pool.NodeIDs, node) {
			status, err := ipam.HostStatus(r.Context(), s.hosts, node)
			if err != nil {
				networkError(w, err)
				return
			}
			if status.Change != nil || status.Config.DefaultPoolID == id {
				writeError(w, http.StatusConflict, "pool is still a host default")
				return
			}
		}
	}
	if err := s.saveNetworkPool(r.Context(), &pool); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pool)
}

func (s *Server) handleDeleteNetworkPool(w http.ResponseWriter, r *http.Request) {
	if s.workloadDB != nil && s.workloadDB.LocalPath() != "" {
		rows, err := s.workloadDB.Query(r.Context(), rqlite.Statement{SQL: `SELECT 1 FROM network_allocations WHERE pool_id=? LIMIT 1`, Params: []any{chi.URLParam(r, "id")}})
		if err != nil {
			networkError(w, err)
			return
		}
		if len(rows.Values) > 0 {
			writeError(w, 409, "pool has active local allocations")
			return
		}
	}
	pools, err := ipam.ListPools(r.Context(), s.db)
	if err != nil {
		networkError(w, err)
		return
	}
	id := chi.URLParam(r, "id")
	for _, pool := range pools {
		if pool.ID == id {
			if s.cfg.LocalNodeID != "" && (len(pool.NodeIDs) != 1 || pool.NodeIDs[0] != s.cfg.LocalNodeID) {
				writeError(w, http.StatusConflict, "a remote node's range remains reserved; remove it through its owner")
				return
			}
			for _, node := range pool.NodeIDs {
				status, err := ipam.HostStatus(r.Context(), s.hosts, node)
				if err != nil {
					networkError(w, err)
					return
				}
				if status.Change != nil || status.Config.DefaultPoolID == id {
					writeError(w, http.StatusConflict, "pool is referenced by host settings or a pending change")
					return
				}
			}
		}
	}
	if err := ipam.DeletePool(r.Context(), s.db, chi.URLParam(r, "id")); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
