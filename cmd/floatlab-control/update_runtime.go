package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func superviseControllers(ctx context.Context, start func(context.Context), log *zap.Logger) {
	dir := os.Getenv("FLOATLAB_ACTIVATION_DIR")
	var cancel context.CancelFunc
	var done chan struct{}
	defer func() {
		if cancel != nil {
			cancel()
			<-done
		}
	}()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		blocked := false
		if dir != "" {
			_, err := os.Stat(filepath.Join(dir, "blocked"))
			blocked = err == nil || !os.IsNotExist(err)
		}
		if blocked && cancel != nil {
			cancel()
			select {
			case <-done:
				cancel = nil
			case <-ctx.Done():
				return
			}
			if err := os.WriteFile(filepath.Join(dir, "controllers-drained"), blockedToken(dir), 0600); err != nil {
				log.Error("management drain acknowledgement", zap.Error(err))
			}
		}
		if !blocked && cancel == nil {
			if dir != "" {
				_ = os.Remove(filepath.Join(dir, "controllers-drained"))
			}
			var child context.Context
			child, cancel = context.WithCancel(ctx)
			done = make(chan struct{})
			go func() { defer close(done); start(child) }()
		}
		if blocked && cancel == nil && dir != "" {
			_ = os.WriteFile(filepath.Join(dir, "controllers-drained"), blockedToken(dir), 0600)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func serveRuntimeChecks(ctx context.Context, node *floatraft.Node) error {
	listener, err := net.Listen("tcp", "127.0.0.1:18081")
	if err != nil {
		return err
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !node.IsLeader() || len(node.Peers()) != 1 {
			http.Error(w, "standalone Raft is not ready", 503)
			return
		}
		if err := node.Barrier(5 * time.Second); err != nil {
			http.Error(w, "Raft barrier failed", 503)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": release.Version, "raft_ready": true})
	})
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }()
	go func() { _ = srv.Serve(listener) }()
	return nil
}

func selfTest(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if expectedRelease != "" && release.Version != expectedRelease {
		return fmt.Errorf("candidate binary version mismatch")
	}
	request := func(address, method, body, contentType string) ([]byte, error) {
		req, e := http.NewRequestWithContext(ctx, method, address, strings.NewReader(body))
		if e != nil {
			return nil, e
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		client := &http.Client{Timeout: 8 * time.Second}
		resp, e := client.Do(req)
		if e != nil {
			return nil, e
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if e != nil {
			return nil, e
		}
		if resp.StatusCode >= 300 {
			return nil, fmt.Errorf("self-test %s returned %d", address, resp.StatusCode)
		}
		return b, nil
	}
	b, e := request("http://127.0.0.1:18081/", "GET", "", "")
	if e != nil {
		return e
	}
	var runtime struct {
		Version string `json:"version"`
		Ready   bool   `json:"raft_ready"`
	}
	if e = json.Unmarshal(b, &runtime); e != nil || !runtime.Ready || runtime.Version != release.Version {
		return fmt.Errorf("running control process version/Raft mismatch")
	}
	if _, e = request("http://127.0.0.1:8080/", "GET", "", ""); e != nil {
		return fmt.Errorf("UI asset loading: %w", e)
	}
	if _, e = request("http://127.0.0.1:8080/api/v1/health/ready", "GET", "", ""); e != nil {
		return fmt.Errorf("management API readiness: %w", e)
	}
	db := rqlite.NewClient(rqliteURL)
	if e = db.Execute(ctx, []rqlite.Statement{{SQL: "CREATE TABLE IF NOT EXISTS _floatlab_update_selftest (token TEXT PRIMARY KEY, value TEXT NOT NULL)"}}); e != nil {
		return e
	}
	token := selfTestToken
	if token == "" {
		token = "health"
	}
	if selfTestPersistence != "verify" {
		if e = db.Execute(ctx, []rqlite.Statement{{SQL: "INSERT OR REPLACE INTO _floatlab_update_selftest (token,value) VALUES (?,?)", Params: []any{token, "persisted"}}}); e != nil {
			return e
		}
	}
	row, e := db.QueryStrong(ctx, rqlite.Statement{SQL: "SELECT value FROM _floatlab_update_selftest WHERE token=?", Params: []any{token}})
	if e != nil {
		return e
	}
	if len(row.Values) != 1 || len(row.Values[0]) != 1 || row.Values[0][0] != "persisted" {
		return fmt.Errorf("database read/write/restart persistence failed")
	}
	if _, e = request(vmetricsURL+"/health", "GET", "", ""); e != nil {
		return e
	}
	if _, e = request(vlogsURL+"/health", "GET", "", ""); e != nil {
		return e
	}
	host := ipc.NewClient(hostSocket)
	if e = host.Connect(ctx); e != nil {
		return e
	}
	defer host.Close()
	if _, e = host.Execute(ctx, "sys.version", nil); e != nil {
		return e
	}
	// Sidecar writes are confined to sandbox datasets; production health only reads them.
	if isolationTest {
		query := "floatlab_update_selftest{token=" + strconv.Quote(token) + "}"
		if selfTestPersistence != "verify" {
			if _, e = request(vmetricsURL+"/api/v1/import/prometheus", "POST", query+" 1\n", "text/plain"); e != nil {
				return e
			}
			entry, _ := json.Marshal(map[string]string{"component": "update-selftest", "token": token, "_msg": "preflight-" + token})
			if _, e = request(vlogsURL+"/insert/jsonline?_stream_fields=component", "POST", string(entry)+"\n", "application/stream+json"); e != nil {
				return e
			}
		}
		metric, e := request(vmetricsURL+"/api/v1/query?nocache=1&query="+url.QueryEscape(query), "GET", "", "")
		if e != nil {
			return e
		}
		if e = checkMetricSample(metric, token); e != nil {
			return e
		}
		logRows, e := request(vlogsURL+"/select/logsql/query", "POST", "query="+url.QueryEscape("token:"+strconv.Quote(token))+"&limit=10", "application/x-www-form-urlencoded")
		if e != nil {
			return e
		}
		if e = checkLogSample(logRows, token); e != nil {
			return e
		}
		client := ipc.NewClient(hostSocket)
		if e = client.Connect(ctx); e != nil {
			return e
		}
		defer client.Close()
		if _, e = client.Execute(ctx, "sys.version", nil); e != nil {
			return e
		}
		if _, e = client.Execute(ctx, "fs.snapshot.create", map[string]string{"dataset": "floatlab/system/management", "name": "forbidden"}); e == nil {
			return fmt.Errorf("restricted IPC allowed mutation")
		} else if rpc, ok := e.(*ipc.RPCError); !ok || rpc.Code != "cmd.unknown" {
			return fmt.Errorf("mutation was not denied by registry: %w", e)
		}
		for _, address := range append(isolationProbes, "1.1.1.1:443", "8.8.8.8:53", "[2606:4700:4700::1111]:443") {
			conn, e := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
			if e == nil {
				conn.Close()
				return fmt.Errorf("sandbox egress is reachable: %s", address)
			}
		}
		dnsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if ips, e := net.DefaultResolver.LookupHost(dnsCtx, "example.com"); e == nil && len(ips) > 0 {
			return fmt.Errorf("sandbox external DNS resolves")
		}
	}
	if selfTestPersistence != "write" {
		if e = db.Execute(ctx, []rqlite.Statement{{SQL: "DELETE FROM _floatlab_update_selftest WHERE token=?", Params: []any{token}}}); e != nil {
			return e
		}
	}
	return nil
}

func checkMetricSample(b []byte, token string) error {
	var result struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string `json:"metric"`
				Value  []json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if e := json.Unmarshal(b, &result); e != nil {
		return e
	}
	if result.Status != "success" || result.Data.ResultType != "vector" {
		return fmt.Errorf("sidecar metric query failed")
	}
	for _, row := range result.Data.Result {
		if row.Metric["token"] == token && len(row.Value) == 2 {
			var value string
			if e := json.Unmarshal(row.Value[1], &value); e == nil && value == "1" {
				return nil
			}
		}
	}
	return fmt.Errorf("sidecar metric sample missing after write/restart")
}
func checkLogSample(b []byte, token string) error {
	decoder := json.NewDecoder(strings.NewReader(string(b)))
	for {
		var row map[string]string
		e := decoder.Decode(&row)
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if row["token"] == token && row["_msg"] == "preflight-"+token {
			return nil
		}
	}
	return fmt.Errorf("sidecar log sample missing after write/restart")
}

func printReleaseInfo() error {
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"version": release.Version, "required_host": release.RequiredHost, "update_protocol": 1})
}

func blockedToken(dir string) []byte { b, _ := os.ReadFile(filepath.Join(dir, "blocked")); return b }
