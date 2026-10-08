package control

import (
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
	"time"
)

const notificationProjection = `SELECT id,alert_id,stack_id,node_id,kind,severity,title,body,state,created_at,resolved_at,silenced_until FROM notifications`

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	query := notificationProjection + " WHERE 1=1"
	args := []interface{}{}
	for _, field := range []string{"state", "severity", "stack_id"} {
		if value := r.URL.Query().Get(field); value != "" {
			query += " AND " + field + "=?"
			args = append(args, value)
		}
	}
	if since := r.URL.Query().Get("since"); since != "" {
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			writeError(w, 400, "invalid since timestamp")
			return
		}
		query += " AND julianday(created_at)>julianday(?)"
		args = append(args, since)
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, 400, "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	args = append(args, limit)
	s.writeObjects(w, r, query+" ORDER BY created_at DESC LIMIT ?", args...)
}

func (s *Server) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	var n notify.Notification
	if !decodeBody(w, r, &n) {
		return
	}
	if !validSeverity(n.Severity) || n.Title == "" || n.Body == "" {
		writeError(w, 400, "severity, title and body are required")
		return
	}
	n.ID = ""
	n.State = "unread"
	n.ResolvedAt = nil
	if err := notify.Create(r.Context(), s.db, s.broker, &n); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 201, n)
}
func (s *Server) handleGetNotification(w http.ResponseWriter, r *http.Request) {
	s.writeObjects(w, r, notificationProjection+" WHERE id=?", chi.URLParam(r, "id"))
}
func (s *Server) handleReadNotification(w http.ResponseWriter, r *http.Request) {
	s.updateNotification(w, r, "read", nil)
}
func (s *Server) handleResolveNotification(w http.ResponseWriter, r *http.Request) {
	s.updateNotification(w, r, "resolved", nil)
}
func (s *Server) handleSilenceNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Until string `json:"until"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	until, err := time.Parse(time.RFC3339, body.Until)
	if err != nil || !until.After(time.Now()) {
		writeError(w, 400, "until must be a future RFC3339 timestamp")
		return
	}
	s.updateNotification(w, r, "silenced", until.UTC().Format(time.RFC3339Nano))
}
func (s *Server) updateNotification(w http.ResponseWriter, r *http.Request, state string, until interface{}) {
	var resolved interface{}
	if state == "resolved" {
		resolved = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := s.db.Request(r.Context(), rqlite.Statement{SQL: `UPDATE notifications SET state=?,silenced_until=?,resolved_at=? WHERE id=?`, Params: []interface{}{state, until, resolved, chi.URLParam(r, "id")}})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if result.RowsAffected == 0 {
		writeError(w, 404, "notification not found")
		return
	}
	s.handleGetNotification(w, r)
}
func (s *Server) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Request(r.Context(), rqlite.Statement{SQL: `DELETE FROM notifications WHERE id=?`, Params: []interface{}{chi.URLParam(r, "id")}})
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if result.RowsAffected == 0 {
		writeError(w, 404, "notification not found")
		return
	}
	w.WriteHeader(204)
}
