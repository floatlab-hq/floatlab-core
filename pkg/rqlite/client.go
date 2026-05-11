package rqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a thin HTTP client for the rqlite /db/execute and /db/query REST API.
// It handles leader redirection (301 Moved Permanently) transparently.
type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type QueryResult struct {
	Columns []string        `json:"columns"`
	Types   []string        `json:"types"`
	Values  [][]interface{} `json:"values"`
}

type executeResponse struct {
	Results []struct {
		LastInsertID int64  `json:"last_insert_id"`
		RowsAffected int64  `json:"rows_affected"`
		Error        string `json:"error,omitempty"`
	} `json:"results"`
}

type queryResponse struct {
	Results []struct {
		Columns []string        `json:"columns"`
		Types   []string        `json:"types"`
		Values  [][]interface{} `json:"values,omitempty"`
		Error   string          `json:"error,omitempty"`
	} `json:"results"`
}

// Execute runs one or more write statements (INSERT, UPDATE, DELETE, CREATE TABLE).
// Statements are executed as a transaction when transaction=true.
func (c *Client) Execute(ctx context.Context, stmts []Statement) error {
	payload, err := json.Marshal(stmtsToPayload(stmts))
	if err != nil {
		return fmt.Errorf("rqlite: marshal: %w", err)
	}

	endpoint := c.baseURL + "/db/execute?transaction"
	resp, err := c.doPost(ctx, endpoint, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var execResp executeResponse
	if err := json.NewDecoder(resp.Body).Decode(&execResp); err != nil {
		return fmt.Errorf("rqlite: decode execute response: %w", err)
	}
	for _, r := range execResp.Results {
		if r.Error != "" {
			return fmt.Errorf("rqlite: execute error: %s", r.Error)
		}
	}
	return nil
}

// Query runs a read statement and returns the result set.
func (c *Client) Query(ctx context.Context, stmt Statement) (*QueryResult, error) {
	payload, err := json.Marshal(stmtsToPayload([]Statement{stmt}))
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal: %w", err)
	}

	endpoint := c.baseURL + "/db/query?level=weak"
	resp, err := c.doPost(ctx, endpoint, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var qResp queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&qResp); err != nil {
		return nil, fmt.Errorf("rqlite: decode query response: %w", err)
	}
	if len(qResp.Results) == 0 {
		return &QueryResult{}, nil
	}
	r := qResp.Results[0]
	if r.Error != "" {
		return nil, fmt.Errorf("rqlite: query error: %s", r.Error)
	}
	return &QueryResult{
		Columns: r.Columns,
		Types:   r.Types,
		Values:  r.Values,
	}, nil
}

func (c *Client) doPost(ctx context.Context, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rqlite: http: %w", err)
	}

	// rqlite returns 301 to redirect writes to the leader.
	if resp.StatusCode == http.StatusMovedPermanently {
		resp.Body.Close()
		location := resp.Header.Get("Location")
		if location == "" {
			return nil, fmt.Errorf("rqlite: redirect with no Location header")
		}
		u, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("rqlite: parse redirect URL: %w", err)
		}
		// Update base URL to leader and retry once.
		c.baseURL = u.Scheme + "://" + u.Host
		req2, _ := http.NewRequestWithContext(ctx, http.MethodPost, location, bytes.NewReader(body))
		req2.Header.Set("Content-Type", "application/json")
		resp2, err := c.http.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("rqlite: redirect retry: %w", err)
		}
		return resp2, nil
	}

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("rqlite: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

// Statement is a parameterized SQL statement.
type Statement struct {
	SQL    string
	Params []interface{}
}

func stmtsToPayload(stmts []Statement) interface{} {
	payload := make([]interface{}, len(stmts))
	for i, s := range stmts {
		if len(s.Params) == 0 {
			payload[i] = []interface{}{s.SQL}
		} else {
			row := make([]interface{}, 0, 1+len(s.Params))
			row = append(row, s.SQL)
			row = append(row, s.Params...)
			payload[i] = row
		}
	}
	return payload
}
