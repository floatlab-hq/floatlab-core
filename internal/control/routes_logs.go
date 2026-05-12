package control

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/floatlab/floatlab-core/pkg/logs"
)

// frontendLogLine matches the frontend LogLine interface.
type frontendLogLine struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Stream    string `json:"stream,omitempty"`
	Container string `json:"container,omitempty"`
}

func toFrontendLines(raw []logs.LogLine) []frontendLogLine {
	out := make([]frontendLogLine, 0, len(raw))
	for _, l := range raw {
		level := l.Level
		if level == "" {
			level = "info"
		}
		out = append(out, frontendLogLine{
			Timestamp: l.Time,
			Level:     level,
			Message:   l.Msg,
			Container: l.ContainerName,
		})
	}
	return out
}

func registerLogRoutes(r chi.Router, s *Server) {
	r.Get("/logs/search", s.handleLogSearch)
	r.Get("/logs/audit", s.handleLogAudit)
	r.Get("/logs/stacks/{stack_id}", s.handleStackLogs)
	r.Get("/logs/containers/{container_id}", s.handleContainerLogs)
	r.Get("/logs/nodes/{node_id}", s.handleNodeLogs)
}

func (s *Server) handleLogSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("q")
	if query == "" {
		query = "*"
	}
	window := q.Get("range")
	if window == "" {
		window = "1h"
	}
	limitStr := q.Get("limit")
	limit, _ := strconv.Atoi(limitStr)

	start, end, _ := rangeToWindow(window)
	lines, err := s.vlogs.Query(r.Context(), query, start, end, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "log query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFrontendLines(lines))
}

func (s *Server) handleLogAudit(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit, _ := strconv.Atoi(limitStr)
	if limit == 0 {
		limit = 50
	}
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	lines, err := s.vlogs.Query(r.Context(), `app:"floatlab-control" | kind:"audit"`, start, end, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "audit query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFrontendLines(lines))
}

func (s *Server) handleStackLogs(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	if r.URL.Query().Get("follow") == "true" {
		logs.ProxyTail(r.Context(), s.vlogs, w, `stack_id:"`+stackID+`"`)
		return
	}
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, _ := rangeToWindow(window)
	lines, err := s.vlogs.Query(r.Context(), `stack_id:"`+stackID+`"`, start, end, 500)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFrontendLines(lines))
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	containerID := chi.URLParam(r, "container_id")
	if r.URL.Query().Get("follow") == "true" {
		logs.ProxyTail(r.Context(), s.vlogs, w, `container_id:"`+containerID+`"`)
		return
	}
	start, end, _ := rangeToWindow("1h")
	lines, err := s.vlogs.Query(r.Context(), `container_id:"`+containerID+`"`, start, end, 500)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFrontendLines(lines))
}

func (s *Server) handleNodeLogs(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, _ := rangeToWindow(window)
	lines, err := s.vlogs.Query(r.Context(), `node_id:"`+nodeID+`"`, start, end, 500)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toFrontendLines(lines))
}

func rangeToWindow(r string) (start, end time.Time, step string) {
	end = time.Now().UTC()
	switch r {
	case "6h":
		start = end.Add(-6 * time.Hour)
		step = "5m"
	case "24h":
		start = end.Add(-24 * time.Hour)
		step = "15m"
	case "7d":
		start = end.Add(-7 * 24 * time.Hour)
		step = "1h"
	default:
		start = end.Add(-1 * time.Hour)
		step = "1m"
	}
	return
}
