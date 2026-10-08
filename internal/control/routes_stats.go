package control

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/floatlab/floatlab-core/pkg/stats"
)

func registerStatsRoutes(r chi.Router, s *Server) {
	r.Get("/stats/query", s.handleStatsQuery)
	r.Get("/stats/nodes/{node_id}", s.handleNodeStats)
	r.Get("/stats/stacks/{stack_id}", s.handleStackStats)
	r.Get("/stats/storage/{node_id}", s.handleStorageStats)
}

// metricPoint matches the frontend MetricPoint interface: {timestamp, value}.
type metricPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// metricSeries matches the frontend MetricSeries interface: {label, unit, points}.
type metricSeries struct {
	Label  string        `json:"label"`
	Unit   string        `json:"unit"`
	Points []metricPoint `json:"points"`
}

func toMetricSeries(label, unit string, s stats.Series) metricSeries {
	pts := make([]metricPoint, 0, len(s.Points))
	for _, p := range s.Points {
		pts = append(pts, metricPoint{Timestamp: p.Time.Unix(), Value: p.Value})
	}
	return metricSeries{Label: label, Unit: unit, Points: pts}
}

func (s *Server) handleStatsWebhook(w http.ResponseWriter, r *http.Request) {
	stats.WebhookHandler(s.db, s.broker, s.log)(w, r)
}

func (s *Server) handleStatsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := q.Get("query")
	if query == "" {
		query = q.Get("q")
	}
	if query == "" {
		writeError(w, http.StatusBadRequest, "query parameter required")
		return
	}
	window := q.Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)
	if raw := q.Get("start"); raw != "" {
		parsed, err := parseMetricTime(raw)
		if err != nil {
			writeError(w, 400, "invalid start timestamp")
			return
		}
		start = parsed
	}
	if raw := q.Get("end"); raw != "" {
		parsed, err := parseMetricTime(raw)
		if err != nil {
			writeError(w, 400, "invalid end timestamp")
			return
		}
		end = parsed
	}
	if start.After(end) {
		writeError(w, 400, "start must not be after end")
		return
	}
	if raw := q.Get("step"); raw != "" {
		duration, err := time.ParseDuration(raw)
		if err != nil || duration <= 0 {
			writeError(w, 400, "invalid step")
			return
		}
		step = raw
	}
	series, err := s.vmets.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		writeError(w, http.StatusBadGateway, "metrics query failed: "+err.Error())
		return
	}
	// Return raw VictoriaMetrics series for ad-hoc queries.
	writeJSON(w, http.StatusOK, map[string]interface{}{"query": query, "series": publicMetricSeries(query, series)})
}

func (s *Server) handleNodeStats(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeError(w, 404, "node not found")
		return
	}
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)
	result := map[string]interface{}{}
	for name, query := range map[string]string{
		"cpu_usage_percent":  `100 - (avg by(node_id)(rate(node_cpu_seconds_total{mode="idle",node_id="` + nodeID + `"}[5m])) * 100)`,
		"memory_used_bytes":  `node_memory_MemTotal_bytes{node_id="` + nodeID + `"} - node_memory_MemAvailable_bytes{node_id="` + nodeID + `"}`,
		"memory_total_bytes": `node_memory_MemTotal_bytes{node_id="` + nodeID + `"}`,
	} {
		series, err := s.vmets.QueryRange(r.Context(), query, start, end, step)
		if err != nil {
			writeError(w, 502, "metrics query failed")
			return
		}
		public := publicMetricSeries(name, series)
		if len(public) > 0 {
			result[name] = public[0]
		}
	}
	writeJSON(w, 200, map[string]interface{}{"node_id": nodeID, "range": window, "series": result})
}

func (s *Server) handleStackStats(w http.ResponseWriter, r *http.Request) {
	stackID := chi.URLParam(r, "stack_id")
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)

	queries := []struct{ label, unit, query string }{
		{"cpu", "%", `sum by(stack_id)(rate(container_cpu_usage_seconds_total{stack_id="` + stackID + `"}[5m])) * 100`},
		{"mem", "bytes", `sum by(stack_id)(container_memory_usage_bytes{stack_id="` + stackID + `"})`},
		{"net_rx", "bytes/s", `sum by(stack_id)(rate(container_network_receive_bytes_total{stack_id="` + stackID + `"}[5m]))`},
		{"net_tx", "bytes/s", `sum by(stack_id)(rate(container_network_transmit_bytes_total{stack_id="` + stackID + `"}[5m]))`},
		{"disk_read", "bytes/s", `sum by(stack_id)(rate(container_blkio_device_usage_total{op="Read",stack_id="` + stackID + `"}[5m]))`},
		{"disk_write", "bytes/s", `sum by(stack_id)(rate(container_blkio_device_usage_total{op="Write",stack_id="` + stackID + `"}[5m]))`},
	}

	result := make([]metricSeries, 0, len(queries))
	for _, mq := range queries {
		series, err := s.vmets.QueryRange(r.Context(), mq.query, start, end, step)
		if err != nil {
			result = append(result, metricSeries{Label: mq.label, Unit: mq.unit, Points: []metricPoint{}})
			continue
		}
		if len(series) == 0 {
			result = append(result, metricSeries{Label: mq.label, Unit: mq.unit, Points: []metricPoint{}})
			continue
		}
		result = append(result, toMetricSeries(mq.label, mq.unit, series[0]))
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleStorageStats(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	if _, err := s.store.GetNode(r.Context(), nodeID); err != nil {
		writeError(w, 404, "node not found")
		return
	}
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)
	series, err := s.vmets.QueryRange(r.Context(), `zfs_pool_free_bytes{node_id="`+nodeID+`"}`, start, end, step)
	if err != nil {
		writeError(w, 502, "metrics query failed")
		return
	}
	result := map[string]interface{}{}
	public := publicMetricSeries("zfs_pool_free_bytes", series)
	if len(public) > 0 {
		result["zfs_pool_free_bytes"] = public[0]
	}
	writeJSON(w, 200, map[string]interface{}{"node_id": nodeID, "range": window, "series": result})
}

func publicMetricSeries(name string, series []stats.Series) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(series))
	for _, s := range series {
		points := make([]map[string]interface{}, 0, len(s.Points))
		for _, point := range s.Points {
			points = append(points, map[string]interface{}{"ts": point.Time.UnixMilli(), "value": point.Value})
		}
		label := name
		if metric := s.Labels["__name__"]; metric != "" {
			label = metric
		}
		labels := s.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		result = append(result, map[string]interface{}{"name": label, "labels": labels, "points": points})
	}
	return result
}
func parseMetricTime(value string) (time.Time, error) {
	if timestamp, err := strconv.ParseFloat(value, 64); err == nil {
		return time.Unix(int64(timestamp), int64((timestamp-float64(int64(timestamp)))*1e9)).UTC(), nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
