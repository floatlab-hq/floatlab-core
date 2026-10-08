package control

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

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
	writeJSON(w, 200, map[string]interface{}{"status": "ok", "version": "0.1.0", "uptime_seconds": int64(time.Since(s.startedAt).Seconds()), "raft_state": s.raft.State().String(), "leader": s.raft.Leader()})
}

func (s *Server) handleHealthReady(w http.ResponseWriter, r *http.Request) {
	state := s.raft.State()
	raftOK := state == hraft.Leader || state == hraft.Follower
	_, dbErr := s.db.Query(r.Context(), rqlite.Statement{SQL: "SELECT 1"})
	hostOK := false
	nodes, err := s.store.ListNodes(r.Context())
	if err == nil {
		for _, node := range nodes {
			if _, err := s.hosts.Execute(r.Context(), node.ID, "sys.info", nil); err == nil {
				hostOK = true
				break
			}
		}
	}
	ready := raftOK && dbErr == nil && hostOK
	status, label := 503, "not_ready"
	if ready {
		status, label = 200, "ready"
	}
	writeJSON(w, status, map[string]interface{}{"status": label, "ready": ready, "checks": map[string]interface{}{"raft": map[string]bool{"ok": raftOK}, "rqlite": map[string]bool{"ok": dbErr == nil}, "hostd": map[string]bool{"ok": hostOK}}})
}

func (s *Server) handleHealthRaft(w http.ResponseWriter, r *http.Request) {
	stats := s.raft.Stats()
	commit, _ := strconv.ParseInt(stats["commit_index"], 10, 64)
	last, _ := strconv.ParseInt(stats["last_log_index"], 10, 64)
	applied, _ := strconv.ParseInt(stats["applied_index"], 10, 64)
	writeJSON(w, 200, map[string]interface{}{"leader": s.raft.Leader(), "state": s.raft.State().String(), "peers": s.raft.Peers(), "commit_index": commit, "last_log_index": last, "applied_index": applied})
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
	if n.Name == "" || n.Hostname == "" || n.ZFSPool == "" || (n.Role != "" && n.Role != "primary" && n.Role != "secondary") {
		writeError(w, 400, "name, hostname, zfs_pool and valid role are required")
		return
	}
	n.Status = "offline"
	if err := s.store.CreateNode(r.Context(), &n); err != nil {
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
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
	if body.Name == "" {
		body.Name = existing.Name
	}
	if body.Hostname == "" {
		body.Hostname = existing.Hostname
	}
	if body.ZFSPool == "" {
		body.ZFSPool = existing.ZFSPool
	}
	if body.Role == "" {
		body.Role = existing.Role
	}
	if body.Addresses == nil {
		body.Addresses = existing.Addresses
	}
	body.ClusterUUID = existing.ClusterUUID
	if body.Role != "primary" && body.Role != "secondary" {
		writeError(w, 400, "invalid role")
		return
	}
	body.Status = "unknown"
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
	if _, err := s.store.GetNode(r.Context(), id); err != nil {
		writeError(w, 404, "node not found")
		return
	}
	stacks, err := s.store.ListStacks(r.Context())
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, stack := range stacks {
		if stack.PrimaryNodeID == id || stack.BackupNodeID == id {
			writeError(w, 409, "node has stack assignments")
			return
		}
	}
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
	result := make([]*config.Stack, 0)
	for _, st := range all {
		if st.PrimaryNodeID == id || st.BackupNodeID == id {
			result = append(result, st)
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func registerNetworkRoutes(r chi.Router, s *Server) {
	r.Group(func(r chi.Router) {
		r.Use(s.requireAdminJWT)
		r.Use(s.idempotency)
		r.Get("/settings/network-pools", s.handleListNetworkPools)
		r.Post("/settings/network-pools", s.handleCreateNetworkPool)
		r.Put("/settings/network-pools/{id}", s.handleUpdateNetworkPool)
		r.Delete("/settings/network-pools/{id}", s.handleDeleteNetworkPool)
		r.Get("/network/pools", s.handleListPrefixPools)
		r.Post("/network/pools", s.handleCreatePrefixPool)
		r.Post("/network/allocations", s.handleCreateAllocation)
		r.Get("/network/allocations", s.handleListAllocations)
		r.Delete("/network/allocations/{id}", s.handleDeleteAllocation)
	})
}
func registerNotifyRoutes(r chi.Router, s *Server) {
	r.Get("/notifications", s.handleListNotifications)
	r.Post("/notifications", s.handleCreateNotification)
	r.Get("/notifications/{id}", s.handleGetNotification)
	r.Post("/notifications/{id}/read", s.handleReadNotification)
	r.Post("/notifications/{id}/silence", s.handleSilenceNotification)
	r.Post("/notifications/{id}/resolve", s.handleResolveNotification)
	r.Delete("/notifications/{id}", s.handleDeleteNotification)
}
func registerEventRoutes(r chi.Router, s *Server) { r.Get("/events", s.handleEvents) }

func (s *Server) handleListAllocations(w http.ResponseWriter, r *http.Request) {
	stackID := r.URL.Query().Get("stack_id")
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT id,stack_id,service,address,prefix_pool,allocated_at,'','' FROM ip_reservations WHERE (? = '' OR stack_id = ?)
 UNION ALL SELECT id,stack_id,'',address,'',created_at,pool_id,state FROM network_allocations WHERE (? = '' OR stack_id = ?) ORDER BY 6 DESC`,
		Params: []interface{}{stackID, stackID, stackID, stackID},
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
		PoolID      string `json:"pool_id,omitempty"`
		State       string `json:"state,omitempty"`
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
		a.PoolID, _ = row[6].(string)
		a.State, _ = row[7].(string)
		rows = append(rows, a)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleDeleteAllocation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	result, err := s.db.Request(r.Context(), rqlite.Statement{SQL: `DELETE FROM ip_reservations WHERE id=?`, Params: []interface{}{id}})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if result.RowsAffected == 0 {
		managed, err := s.queryObjects(r, `SELECT id FROM network_allocations WHERE id=?`, id)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if len(managed) > 0 {
			writeError(w, 409, "managed workload addresses must be released through stack deletion")
			return
		}
		writeError(w, 404, "allocation not found")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleNodeHealth(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetNode(r.Context(), id); err != nil {
		writeError(w, 404, "node not found")
		return
	}
	started := time.Now()
	_, err := s.hosts.Execute(r.Context(), id, "sys.info", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "unreachable", "latency_ms": 0})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "reachable", "latency_ms": time.Since(started).Milliseconds()})
}

func stub(v interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, v)
	}
}
