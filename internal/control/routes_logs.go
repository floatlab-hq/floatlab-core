package control

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/logs"
	"github.com/go-chi/chi/v5"
)

// apiLogLine is the public OpenAPI representation, independent of VictoriaLogs fields.
type apiLogLine struct {
	Timestamp string            `json:"ts"`
	Stream    string            `json:"stream"`
	Level     string            `json:"level,omitempty"`
	Message   string            `json:"msg"`
	Labels    map[string]string `json:"labels,omitempty"`
}

func toAPILines(raw []logs.LogLine) []apiLogLine {
	out := make([]apiLogLine, 0, len(raw))
	for _, line := range raw {
		out = append(out, toAPILine(line))
	}
	return out
}

func toAPILine(line logs.LogLine) apiLogLine {
	labels := make(map[string]string, len(line.Stream)+4)
	for key, value := range line.Stream {
		labels[key] = value
	}
	for key, value := range map[string]string{"container_name": line.ContainerName, "stack_id": line.StackID, "node_id": line.NodeID, "service": line.Service} {
		if value != "" {
			labels[key] = value
		}
	}
	stream := labels["stream"]
	if stream != "stderr" {
		stream = "stdout"
	}
	return apiLogLine{Timestamp: line.Time, Stream: stream, Level: line.Level, Message: line.Msg, Labels: labels}
}

func registerLogRoutes(r chi.Router, s *Server) {
	r.Get("/logs/search", s.handleLogSearch)
	r.Get("/logs/audit", s.handleLogAudit)
	r.Get("/logs/stacks/{stack_id}", s.handleStackLogs)
	r.Get("/logs/containers/{container_id}", s.handleContainerLogs)
	r.Get("/logs/nodes/{node_id}", s.handleNodeLogs)
}

func (s *Server) handleLogSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("query")
	if query == "" {
		writeError(w, http.StatusBadRequest, "query is required")
		return
	}
	start, end, err := logWindow(r.URL.Query().Get("start"), r.URL.Query().Get("end"), time.Now().UTC().Add(-time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit, err := logLimit(r, 500, 5000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	lines, err := s.vlogs.Query(r.Context(), query, start, end, limit)
	if err != nil {
		if strings.Contains(err.Error(), "status 400") {
			writeError(w, http.StatusBadRequest, "invalid LogsQL query")
		} else {
			writeError(w, http.StatusBadGateway, "log query failed: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, toAPILines(lines))
}

func (s *Server) handleLogAudit(w http.ResponseWriter, r *http.Request) {
	limit, err := logLimit(r, 50, 1000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	end := time.Now().UTC()
	start := end.Add(-24 * time.Hour)
	lines, err := s.vlogs.Query(r.Context(), `app:"floatlab-control" | kind:"audit"`, start, end, limit)
	if err != nil {
		writeError(w, http.StatusBadGateway, "audit query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPILines(lines))
}

func (s *Server) handleStackLogs(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	if _, err := s.store.GetStack(r.Context(), stackID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	start, end, err := logWindow(r.URL.Query().Get("since"), "", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tail, err := logLimit(r, 200, 5000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := `stack_id:"` + logsQLValue(stackID) + `"`
	if service := r.URL.Query().Get("service"); service != "" {
		query += ` service:"` + logsQLValue(service) + `"`
	}
	lines, err := s.vlogs.Query(r.Context(), query, start, end, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPILines(lines))
}

func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	containerID := chi.URLParam(r, "container_id")
	start, end, err := logWindow(r.URL.Query().Get("since"), "", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tail, err := logLimit(r, 50, 1000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	follow, err := optionalBool(r.URL.Query().Get("follow"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := `container_id:"` + logsQLValue(containerID) + `"`
	if follow {
		logs.ProxyTail(r.Context(), s.vlogs, w, query, func(line logs.LogLine) interface{} { return toAPILine(line) })
		return
	}
	lines, err := s.vlogs.Query(r.Context(), query, start, end, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPILines(lines))
}

func (s *Server) handleNodeLogs(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	start, end, err := logWindow(r.URL.Query().Get("since"), "", time.Now().UTC().Add(-time.Hour))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tail, err := logLimit(r, 200, 5000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := `node_id:"` + logsQLValue(nodeID) + `"`
	if unit := r.URL.Query().Get("unit"); unit != "" {
		query += ` unit:"` + logsQLValue(unit) + `"`
	}
	lines, err := s.vlogs.Query(r.Context(), query, start, end, tail)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toAPILines(lines))
}

func logLimit(r *http.Request, defaultValue, maximum int) (int, error) {
	value := r.URL.Query().Get("tail")
	if value == "" {
		value = r.URL.Query().Get("limit")
	}
	if value == "" {
		return defaultValue, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > maximum {
		return 0, fmt.Errorf("limit must be between 1 and %d", maximum)
	}
	return limit, nil
}

func logWindow(startValue, endValue string, defaultStart time.Time) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	start := defaultStart
	end := now
	var err error
	if startValue != "" {
		start, err = parseLogTime(startValue, now)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start timestamp")
		}
	}
	if endValue != "" {
		end, err = parseLogTime(endValue, now)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end timestamp")
		}
	}
	if start.After(end) {
		return time.Time{}, time.Time{}, fmt.Errorf("start must not be after end")
	}
	return start, end, nil
}

func parseLogTime(value string, now time.Time) (time.Time, error) {
	if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return timestamp.UTC(), nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return time.Time{}, fmt.Errorf("invalid timestamp")
	}
	return now.Add(-duration), nil
}

func optionalBool(value string) (bool, error) {
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("follow must be true or false")
	}
	return parsed, nil
}

func logsQLValue(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}
