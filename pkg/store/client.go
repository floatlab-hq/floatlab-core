package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type AgentClient struct {
	base string
	http *http.Client
}

func NewAgentClient(socketPath string) *AgentClient {
	return &AgentClient{
		base: "http://floatlab-agent",
		http: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}},
	}
}

func (c *AgentClient) ListDatasets(ctx context.Context) ([]Dataset, error) {
	var body struct {
		Datasets []Dataset `json:"datasets"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/zfs/dataset", nil, &body); err != nil {
		return nil, err
	}
	return body.Datasets, nil
}

func (c *AgentClient) GetDataset(ctx context.Context, dataset string) (Dataset, error) {
	var out Dataset
	err := c.doJSON(ctx, http.MethodGet, "/zfs/dataset/"+dataset, nil, &out)
	return out, err
}

func (c *AgentClient) CreateDataset(ctx context.Context, dataset string, req CreateDatasetRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.doNoContent(ctx, http.MethodPut, "/zfs/dataset/"+dataset, bytes.NewReader(body))
}

func (c *AgentClient) DeleteDataset(ctx context.Context, dataset string, recursive bool) error {
	return c.doNoContent(ctx, http.MethodDelete, "/zfs/dataset/"+dataset+query(map[string]string{"recursive": boolQuery(recursive)}), nil)
}

func (c *AgentClient) ListSnapshots(ctx context.Context, dataset string, recursive bool) ([]Snapshot, error) {
	var body struct {
		Snapshots []Snapshot `json:"snapshots"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/zfs/snapshots/"+dataset+query(map[string]string{"recursive": boolQuery(recursive)}), nil, &body); err != nil {
		return nil, err
	}
	return body.Snapshots, nil
}

func (c *AgentClient) CreateSnapshot(ctx context.Context, target string, recursive bool) error {
	return c.doNoContent(ctx, http.MethodPut, "/zfs/snapshot/"+target+query(map[string]string{"recursive": boolQuery(recursive)}), nil)
}

func (c *AgentClient) DeleteSnapshot(ctx context.Context, target string, recursive bool) error {
	return c.doNoContent(ctx, http.MethodDelete, "/zfs/snapshot/"+target+query(map[string]string{"recursive": boolQuery(recursive)}), nil)
}

func (c *AgentClient) SendSnapshot(ctx context.Context, target, from string, recursive bool) (*http.Response, error) {
	path := "/zfs/send/" + target + query(map[string]string{"from": from, "recursive": boolQuery(recursive)})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		defer resp.Body.Close()
		return nil, fmt.Errorf("agent send failed: %s", resp.Status)
	}
	return resp, nil
}

func (c *AgentClient) ReceiveSnapshot(ctx context.Context, dataset string, forceRollback bool, body io.Reader) error {
	path := "/zfs/receive/" + dataset + query(map[string]string{"forceRollback": boolQuery(forceRollback)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.base+path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("agent receive failed: %s", resp.Status)
	}
	return nil
}

func RelaySnapshot(ctx context.Context, source, dest *AgentClient, target, from, destDataset string, recursive, forceRollback bool) error {
	resp, err := source.SendSnapshot(ctx, target, from, recursive)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return dest.ReceiveSnapshot(ctx, destDataset, forceRollback, resp.Body)
}

func (c *AgentClient) doJSON(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("agent request failed: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *AgentClient) doNoContent(ctx context.Context, method, path string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("agent request failed: %s", resp.Status)
	}
	return nil
}

func boolQuery(v bool) string {
	if v {
		return "true"
	}
	return ""
}

func query(values map[string]string) string {
	var parts []string
	for k, v := range values {
		if v != "" {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "?" + strings.Join(parts, "&")
}
