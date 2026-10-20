package stats

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// InstantSample is an instant-vector result. ObservedAt is the query result
// timestamp; alert rules obtain source freshness from their explicit freshness query.
type InstantSample struct {
	Labels     map[string]string
	Value      float64
	ObservedAt time.Time
}

// QueryInstant bounds implicit lookback and disables the query cache for alerting.
func (c *Client) QueryInstant(ctx context.Context, query string, at time.Time, maxAge time.Duration) ([]InstantSample, error) {
	params := url.Values{"query": {query}, "time": {strconv.FormatFloat(float64(at.UnixMilli())/1000, 'f', 3, 64)}, "max_lookback": {maxAge.String()}, "nocache": {"1"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.base, "/")+"/api/v1/query?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats: instant query status %d", resp.StatusCode)
	}
	var result struct {
		Status  string `json:"status"`
		Error   string `json:"error"`
		Partial bool   `json:"isPartial"`
		Data    struct {
			Type   string `json:"resultType"`
			Result []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if result.Status != "success" || result.Partial || result.Data.Type != "vector" {
		return nil, fmt.Errorf("stats: expected complete instant vector (%s)", result.Error)
	}
	samples := make([]InstantSample, 0, len(result.Data.Result))
	for _, row := range result.Data.Result {
		if len(row.Value) != 2 {
			return nil, fmt.Errorf("stats: invalid instant sample")
		}
		var value string
		if err := json.Unmarshal(row.Value[1], &value); err != nil {
			return nil, err
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("stats: invalid instant value %q", value)
		}
		samples = append(samples, InstantSample{Labels: row.Metric, Value: v, ObservedAt: at})
	}
	return samples, nil
}
