package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

// ServeBackupPeer is exposed only by the lab listener after current-member mTLS validation.
func (s *Server) ServeBackupPeer(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/internal/backups/prepare":
		var p ipc.BackupRestorePreparePayload
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&p); err != nil || p.BackupNodeID != s.cfg.LocalNodeID {
			writeError(w, 400, "invalid backup preparation request")
			return
		}
		raw, err := s.hosts.Execute(r.Context(), s.cfg.LocalNodeID, "fs.backup.restore.prepare", p)
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	case r.Method == http.MethodGet && r.URL.Path == "/internal/backups/snapshots":
		q := r.URL.Query()
		snaps, err := s.backupSnapshots(r.Context(), s.cfg.LocalNodeID, q.Get("source_node_id"), q.Get("dataset"))
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, snaps)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) backupPeerRequest(ctx context.Context, node, method, path string, payload any, result any) error {
	if s.peerWorkloads == nil {
		return fmt.Errorf("backup node peer transport unavailable")
	}
	origin, ok := s.peerWorkloads.lookup(node)
	u, err := url.Parse(origin)
	if !ok || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return fmt.Errorf("backup node is unavailable")
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, origin+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.peerWorkloads.transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		return fmt.Errorf("backup peer: %s: %s", response.Status, message)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(result)
}

func (s *Server) PrepareBackupRestore(ctx context.Context, p ipc.BackupRestorePreparePayload) (ipc.BackupRestorePrepareResult, error) {
	var result ipc.BackupRestorePrepareResult
	if s.cfg != nil && s.cfg.LocalNodeID != "" && p.BackupNodeID != s.cfg.LocalNodeID {
		err := s.backupPeerRequest(ctx, p.BackupNodeID, http.MethodPost, "/internal/backups/prepare", p, &result)
		return result, err
	}
	raw, err := s.hosts.Execute(ctx, p.BackupNodeID, "fs.backup.restore.prepare", p)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}
