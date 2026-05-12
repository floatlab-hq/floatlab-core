package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to a VictoriaMetrics instance.
type Client struct {
	base string
	hc   *http.Client
}

func NewClient(base string) *Client {
	return &Client{
		base: base,
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

// MetricPoint is a single (timestamp, value) pair.
type MetricPoint struct {
	Time  time.Time `json:"time"`
	Value float64   `json:"value"`
}

// Series is a labelled time series returned by QueryRange.
type Series struct {
	Labels map[string]string `json:"labels"`
	Points []MetricPoint     `json:"points"`
}

type vmRangeResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][]interface{}   `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

// QueryRange executes a PromQL/MetricsQL instant range query against VictoriaMetrics.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step string) ([]Series, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", fmt.Sprintf("%d", start.Unix()))
	params.Set("end", fmt.Sprintf("%d", end.Unix()))
	params.Set("step", step)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/api/v1/query_range?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("stats: query_range: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("stats: query_range: status %d: %s", resp.StatusCode, b)
	}

	var vmResp vmRangeResponse
	if err := json.NewDecoder(resp.Body).Decode(&vmResp); err != nil {
		return nil, fmt.Errorf("stats: decode: %w", err)
	}

	out := make([]Series, 0, len(vmResp.Data.Result))
	for _, r := range vmResp.Data.Result {
		s := Series{Labels: r.Metric, Points: make([]MetricPoint, 0, len(r.Values))}
		for _, v := range r.Values {
			if len(v) != 2 {
				continue
			}
			ts, ok1 := v[0].(float64)
			val, ok2 := v[1].(string)
			if !ok1 || !ok2 {
				continue
			}
			var fval float64
			fmt.Sscanf(val, "%f", &fval)
			s.Points = append(s.Points, MetricPoint{
				Time:  time.Unix(int64(ts), 0).UTC(),
				Value: fval,
			})
		}
		out = append(out, s)
	}
	return out, nil
}

// RangeWindow converts a UI range string ("1h", "6h", "24h", "7d") to
// (start, end, step) suitable for QueryRange.
func RangeWindow(r string) (start, end time.Time, step string) {
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
	default: // "1h"
		start = end.Add(-1 * time.Hour)
		step = "1m"
	}
	return
}
