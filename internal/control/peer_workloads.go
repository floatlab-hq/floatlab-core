package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/go-chi/chi/v5"
)

type workloadPeers struct {
	directory string
	transport http.RoundTripper
	lookup    func(string) (string, bool)
}

func (s *Server) SetWorkloadPeers(directory string, client *http.Client, lookup func(string) (string, bool)) error {
	if client == nil || lookup == nil || s.db.LocalPath() == "" {
		return errors.New("local database and authenticated workload peer transport required")
	}
	if err := s.db.Execute(context.Background(), []rqlite.Statement{{SQL: `CREATE TABLE IF NOT EXISTS workload_operation_owners(operation_id TEXT PRIMARY KEY,node_id TEXT NOT NULL)`}}); err != nil {
		return err
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	s.peerWorkloads = &workloadPeers{directory: directory, transport: transport, lookup: lookup}
	return nil
}

func (s *Server) eachReplica(ctx context.Context, visit func(*rqlite.Client, rqlite.LocalIdentity, time.Time) error) error {
	if s.peerWorkloads == nil {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(s.peerWorkloads.directory, "*.sqlite"))
	if err != nil {
		return err
	}
	for _, path := range files {
		replica, err := rqlite.OpenReplica(path)
		if err != nil {
			return fmt.Errorf("open workload replica: %w", err)
		}
		id, err := replica.LocalIdentity(ctx)
		info, statErr := os.Stat(path)
		if err == nil && statErr != nil {
			err = statErr
		}
		if err == nil && filepath.Base(path) != id.NodeID+".sqlite" {
			err = errors.New("replica filename/source mismatch")
		}
		if err == nil && id.NodeID != s.cfg.LocalNodeID {
			err = visit(replica, id, info.ModTime().UTC())
		}
		replica.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) workloadOwner(ctx context.Context, stackID, operationID string) (string, error) {
	table, column, id := "stacks", "id", stackID
	if operationID != "" {
		table, column, id = "operations", "id", operationID
		mapping, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT node_id FROM workload_operation_owners WHERE operation_id=?`, Params: []any{operationID}})
		if err != nil {
			return "", err
		}
		if len(mapping.Values) > 0 {
			return fmt.Sprint(mapping.Values[0][0]), nil
		}
	}
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT ` + column + ` FROM ` + table + ` WHERE ` + column + `=?`, Params: []any{id}})
	if err != nil {
		return "", err
	}
	if len(rows.Values) > 0 {
		return s.cfg.LocalNodeID, nil
	}
	owner := ""
	err = s.eachReplica(ctx, func(db *rqlite.Client, identity rqlite.LocalIdentity, _ time.Time) error {
		rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT ` + column + ` FROM ` + table + ` WHERE ` + column + `=?`, Params: []any{id}})
		if err != nil {
			return err
		}
		if len(rows.Values) > 0 {
			if owner != "" && owner != identity.NodeID {
				return errors.New("conflicting workload ownership requires reconciliation")
			}
			owner = identity.NodeID
		}
		return nil
	})
	return owner, err
}

func (s *Server) routeWorkloadOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.peerWorkloads == nil {
			next.ServeHTTP(w, r)
			return
		}
		stackID, operationID := chi.URLParam(r, "id"), chi.URLParam(r, "operationId")
		validation := r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stacks/validate")
		if stackID == "" && operationID == "" && !validation && !(r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/stacks")) {
			next.ServeHTTP(w, r)
			return
		}
		owner := ""
		var err error
		if stackID != "" || operationID != "" {
			owner, err = s.workloadOwner(r.Context(), stackID, operationID)
		} else {
			// Creation must choose its owner before any local idempotency or stack writes.
			body, e := io.ReadAll(io.LimitReader(r.Body, 8*1024*1024+1))
			if e != nil || len(body) > 8*1024*1024 {
				writeError(w, 400, "stack request exceeds 8 MiB")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			if validation {
				var request validateStackRequest
				if json.Unmarshal(body, &request) == nil && request.StackID != "" {
					stackID = request.StackID
					owner, err = s.workloadOwner(r.Context(), stackID, "")
				}
			} else {
				var request createStackRequest
				if e = json.Unmarshal(body, &request); e == nil {
					name, slugErr := compose.Slug(request.Name)
					if slugErr != nil {
						writeComposeValidationError(w, slugErr)
						return
					}
					_, parsed, e := validateCompose(r, request.ComposeFile, name)
					if e == nil {
						owner = parsed.Extension.PrimaryNode
					}
				}
			}
		}
		if err != nil {
			writeErrorCode(w, 503, "workload_owner_unavailable", err.Error())
			return
		}
		if owner == "" && r.Header.Get("X-FloatLab-Workload-Hop") != "" && (stackID != "" || operationID != "") {
			writeErrorCode(w, 503, "workload_owner_unavailable", "owner no longer has the requested workload state")
			return
		}
		if owner == "" || owner == s.cfg.LocalNodeID {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-FloatLab-Workload-Hop") != "" {
			writeErrorCode(w, 503, "workload_owner_unavailable", "workload owner could not resolve its local state")
			return
		}
		s.forwardWorkloadOwner(w, r, owner)
	})
}

// routeNodeHealth forwards only the read-only health endpoint to its own node.
func (s *Server) routeNodeHealth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := chi.URLParam(r, "id")
		if s.peerWorkloads == nil || node == s.cfg.LocalNodeID {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-FloatLab-Workload-Hop") != "" {
			writeErrorCode(w, 503, "node_unavailable", "peer health request targets another node")
			return
		}
		s.forwardWorkloadOwner(w, r, node)
	})
}

func (s *Server) forwardWorkloadOwner(w http.ResponseWriter, r *http.Request, owner string) {
	origin, ok := s.peerWorkloads.lookup(owner)
	target, e := url.Parse(origin)
	if !ok || e != nil || target.Host == "" || target.User != nil || target.Scheme != "https" {
		writeErrorCode(w, 503, "workload_owner_unavailable", "workload owner is unavailable: "+owner)
		return
	}
	proxy := &httputil.ReverseProxy{Transport: s.peerWorkloads.transport, Rewrite: func(request *httputil.ProxyRequest) {
		request.SetURL(target)
		request.Out.Header.Set("X-FloatLab-Workload-Hop", s.cfg.LocalNodeID)
	}, ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
		writeErrorCode(w, 503, "workload_owner_unavailable", "cannot reach workload owner: "+owner)
	}}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		proxy.ModifyResponse = func(response *http.Response) error { return s.rememberPeerOperation(response, owner) }
	}
	proxy.ServeHTTP(w, r)
}

type replayBody struct {
	io.Reader
	io.Closer
}

func (s *Server) rememberPeerOperation(response *http.Response, owner string) error {
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil
	}
	operationID := ""
	if location, err := url.Parse(response.Header.Get("Location")); err == nil && strings.HasPrefix(location.Path, "/api/v1/operations/") {
		operationID = strings.TrimPrefix(location.Path, "/api/v1/operations/")
	}
	if operationID == "" && strings.Contains(response.Header.Get("Content-Type"), "application/json") {
		original := response.Body
		body, err := io.ReadAll(io.LimitReader(original, 1024*1024+1))
		if err != nil {
			return err
		}
		response.Body = replayBody{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
		if len(body) <= 1024*1024 {
			var result struct {
				OperationID string `json:"operation_id"`
			}
			if json.Unmarshal(body, &result) == nil {
				operationID = result.OperationID
			}
		}
	}
	if operationID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO workload_operation_owners(operation_id,node_id) VALUES(?,?) ON CONFLICT(operation_id) DO UPDATE SET node_id=excluded.node_id`, Params: []any{operationID, owner}}})
}

func (s *Server) appendReplicaStacks(ctx context.Context, result []stackResponse) ([]stackResponse, error) {
	seen := map[string]bool{}
	for _, stack := range result {
		seen[stack.ID] = true
	}
	err := s.eachReplica(ctx, func(db *rqlite.Client, id rqlite.LocalIdentity, observed time.Time) error {
		stacks, err := config.NewStore(db).ListStacks(ctx)
		if err != nil {
			return err
		}
		states, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT entry FROM workload_lifecycle`})
		if err != nil {
			return err
		}
		stateByID := map[string]string{}
		for _, row := range states.Values {
			var state run.StackStateChanged
			if json.Unmarshal([]byte(fmt.Sprint(row[0])), &state) == nil {
				stateByID[state.StackID] = string(state.To)
			}
		}
		for _, stack := range stacks {
			if seen[stack.ID] {
				return errors.New("conflicting workload ownership requires reconciliation")
			}
			seen[stack.ID] = true
			entry := toStackResponse(stack, stateByID[stack.ID])
			entry.OwnerNode = id.NodeID
			entry.Replica = true
			entry.Stale = true
			entry.ObservedGeneration = id.Generation
			entry.ObservedRevision = id.Revision
			entry.ObservedAt = &observed
			result = append(result, entry)
		}
		return nil
	})
	return result, err
}
