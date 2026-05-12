package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client talks to a VictoriaLogs instance.
type Client struct {
	base   string
	hc     *http.Client
}

func NewClient(base string) *Client {
	return &Client{
		base: base,
		hc:   &http.Client{Timeout: 30 * time.Second},
	}
}

// LogLine is a single JSON-line log entry from VictoriaLogs.
// VictoriaLogs returns all indexed fields at the top level alongside _time and _msg.
type LogLine struct {
	Time   string `json:"_time"`
	Msg    string `json:"_msg"`
	Level  string `json:"level,omitempty"`
	Stream map[string]string `json:"_stream_fields,omitempty"`
	// Common indexed fields pushed by floatlab-hostd / floatlab-control.
	ContainerName string `json:"container_name,omitempty"`
	StackID       string `json:"stack_id,omitempty"`
	NodeID        string `json:"node_id,omitempty"`
}

// Push sends lines to VictoriaLogs /insert/jsonline.
// Each entry is a map of field→value; _msg is the log text.
func (c *Client) Push(ctx context.Context, lines []map[string]string) error {
	var buf bytes.Buffer
	for _, l := range lines {
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/insert/jsonline", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("logs: push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("logs: push: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// Query runs a LogsQL query against VictoriaLogs and returns matching log lines.
// limit defaults to 1000 when 0.
func (c *Client) Query(ctx context.Context, query string, start, end time.Time, limit int) ([]LogLine, error) {
	if limit == 0 {
		limit = 1000
	}
	params := url.Values{}
	params.Set("query", query)
	params.Set("start", start.UTC().Format(time.RFC3339))
	params.Set("end", end.UTC().Format(time.RFC3339))
	params.Set("limit", fmt.Sprintf("%d", limit))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.base+"/select/logsql/query?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("logs: query: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("logs: query: status %d: %s", resp.StatusCode, b)
	}

	dec := json.NewDecoder(resp.Body)
	var lines []LogLine
	for dec.More() {
		var l LogLine
		if err := dec.Decode(&l); err != nil {
			break
		}
		lines = append(lines, l)
	}
	return lines, nil
}

// TailURL returns the URL for the SSE tail endpoint with the given LogsQL query.
// Callers pass this to ProxyTail.
func (c *Client) TailURL(query string) string {
	params := url.Values{}
	params.Set("query", query)
	return c.base + "/select/logsql/tail?" + params.Encode()
}

// BaseURL returns the configured base URL (for constructing proxy URLs).
func (c *Client) BaseURL() string { return c.base }
