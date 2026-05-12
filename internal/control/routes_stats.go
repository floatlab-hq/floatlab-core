package control

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/stats"
)

func registerStatsRoutes(r chi.Router, s *Server) {
	r.Post("/stats/webhook", s.handleStatsWebhook)
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
	query := q.Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter required")
		return
	}
	window := q.Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)
	series, err := s.vmets.QueryRange(r.Context(), query, start, end, step)
	if err != nil {
		writeError(w, http.StatusBadGateway, "metrics query failed: "+err.Error())
		return
	}
	// Return raw VictoriaMetrics series for ad-hoc queries.
	writeJSON(w, http.StatusOK, series)
}

func (s *Server) handleNodeStats(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "node_id")
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)

	queries := []struct{ label, unit, query string }{
		{"cpu", "%", `100 - (avg by(node_id)(rate(node_cpu_seconds_total{mode="idle",node_id="` + nodeID + `"}[5m])) * 100)`},
		{"memory", "bytes", `node_memory_MemTotal_bytes{node_id="` + nodeID + `"} - node_memory_MemAvailable_bytes{node_id="` + nodeID + `"}`},
	}

	result := make([]metricSeries, 0, len(queries))
	for _, mq := range queries {
		series, err := s.vmets.QueryRange(r.Context(), mq.query, start, end, step)
		if err != nil {
			s.log.Warn("stats: node query", zap.String("metric", mq.label), zap.Error(err))
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
	window := r.URL.Query().Get("range")
	if window == "" {
		window = "1h"
	}
	start, end, step := stats.RangeWindow(window)
	series, err := s.vmets.QueryRange(r.Context(),
		`zfs_pool_free_bytes{node_id="`+nodeID+`"}`, start, end, step)
	if err != nil || len(series) == 0 {
		writeJSON(w, http.StatusOK, []metricSeries{})
		return
	}
	writeJSON(w, http.StatusOK, []metricSeries{toMetricSeries("zfs_free", "bytes", series[0])})
}
