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
	"sync"
	"time"
)

// Client is a thin HTTP client for the rqlite /db/execute and /db/query REST API.
// It handles leader redirection (301 Moved Permanently) transparently.
type Client struct {
	mu      sync.RWMutex
	baseURL string
	http    *http.Client
	local   *localDB
}

// ConfigureHTTP switches lab transport after identity enrollment without racing requests.
func (c *Client) ConfigureHTTP(baseURL string, client *http.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.baseURL = strings.TrimRight(baseURL, "/")
	if client != nil {
		c.http = client
	}
}
func (c *Client) endpoint(path string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.baseURL + path
}
func (c *Client) httpClient() *http.Client { c.mu.RLock(); defer c.mu.RUnlock(); return c.http }

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

type QueryResult struct {
	Columns      []string        `json:"columns"`
	Types        []string        `json:"types"`
	Values       [][]interface{} `json:"values"`
	RowsAffected int64           `json:"rows_affected"`
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
		Columns      []string        `json:"columns"`
		Types        []string        `json:"types"`
		Values       [][]interface{} `json:"values,omitempty"`
		RowsAffected int64           `json:"rows_affected,omitempty"`
		Error        string          `json:"error,omitempty"`
	} `json:"results"`
}

// Execute runs one or more write statements (INSERT, UPDATE, DELETE, CREATE TABLE).
// Statements are executed as a transaction when transaction=true.
func (c *Client) Execute(ctx context.Context, stmts []Statement) error {
	if len(stmts) == 0 {
		return nil
	}
	if c.local != nil {
		return c.local.execute(ctx, stmts)
	}
	payload, err := json.Marshal(stmtsToPayload(stmts))
	if err != nil {
		return fmt.Errorf("rqlite: marshal: %w", err)
	}

	endpoint := c.endpoint("/db/execute?transaction")
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
	return c.query(ctx, "/db/query?level=weak", stmt)
}

// QueryStrong reads through the leader so ownership decisions cannot use a stale
// follower snapshot during address takeover or pool edits.
func (c *Client) QueryStrong(ctx context.Context, stmt Statement) (*QueryResult, error) {
	return c.query(ctx, "/db/query?level=strong", stmt)
}

// QueryLocal never forwards a read to the consensus leader.
func (c *Client) QueryLocal(ctx context.Context, stmt Statement) (*QueryResult, error) {
	return c.query(ctx, "/db/query?level=none", stmt)
}

// Request runs a write statement through rqlite's unified request endpoint.
func (c *Client) Request(ctx context.Context, stmt Statement) (*QueryResult, error) {
	return c.query(ctx, "/db/request?transaction", stmt)
}

// QueryBatch reads related tables from one committed snapshot.
func (c *Client) QueryBatch(ctx context.Context, stmts []Statement) ([]*QueryResult, error) {
	if len(stmts) == 0 {
		return nil, nil
	}
	if c.local != nil {
		tx, err := c.local.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		var result []*QueryResult
		for _, s := range stmts {
			rows, err := tx.QueryContext(ctx, s.SQL, s.Params...)
			if err != nil {
				return nil, err
			}
			r, err := collectRows(rows)
			if err != nil {
				return nil, err
			}
			result = append(result, r)
		}
		return result, tx.Commit()
	}
	payload, err := json.Marshal(stmtsToPayload(stmts))
	if err != nil {
		return nil, err
	}
	resp, err := c.doPost(ctx, c.endpoint("/db/query?transaction&level=strong"), payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var decoded queryResponse
	if err = json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, err
	}
	if len(decoded.Results) != len(stmts) {
		return nil, fmt.Errorf("rqlite: incomplete transaction result")
	}
	var result []*QueryResult
	for _, r := range decoded.Results {
		if r.Error != "" {
			return nil, fmt.Errorf("rqlite: transaction query: %s", r.Error)
		}
		result = append(result, &QueryResult{Columns: r.Columns, Types: r.Types, Values: r.Values, RowsAffected: r.RowsAffected})
	}
	return result, nil
}

func (c *Client) query(ctx context.Context, path string, stmt Statement) (*QueryResult, error) {
	if c.local != nil {
		return c.local.query(ctx, stmt, strings.HasPrefix(path, "/db/request"))
	}
	payload, err := json.Marshal(stmtsToPayload([]Statement{stmt}))
	if err != nil {
		return nil, fmt.Errorf("rqlite: marshal: %w", err)
	}

	endpoint := c.endpoint(path)
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
		Columns:      r.Columns,
		Types:        r.Types,
		Values:       r.Values,
		RowsAffected: r.RowsAffected,
	}, nil
}

func (c *Client) doPost(ctx context.Context, endpoint string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rqlite: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := *c.httpClient()
	// Preserve POST bodies on leader redirects and forbid TLS downgrades.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	originalBase := req.URL.Scheme + "://" + req.URL.Host
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("rqlite: http: %w", err)
		}
		if resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusPermanentRedirect {
			resp.Body.Close()
			if attempt != 0 {
				return nil, fmt.Errorf("rqlite: too many leader redirects")
			}
			location := resp.Header.Get("Location")
			u, err := url.Parse(location)
			if err != nil || location == "" {
				return nil, fmt.Errorf("rqlite: invalid leader redirect")
			}
			u = req.URL.ResolveReference(u)
			if u.Scheme != req.URL.Scheme || u.Host == "" || u.User != nil {
				return nil, fmt.Errorf("rqlite: insecure leader redirect")
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			req.Header.Set("Content-Type", "application/json")
			continue
		}
		if resp.StatusCode >= 400 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("rqlite: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		if attempt > 0 {
			c.mu.Lock()
			if c.baseURL == originalBase {
				c.baseURL = req.URL.Scheme + "://" + req.URL.Host
			}
			c.mu.Unlock()
		}
		return resp, nil
	}
	return nil, fmt.Errorf("rqlite: exhausted leader redirects")
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
