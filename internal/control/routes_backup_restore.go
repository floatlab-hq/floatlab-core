package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/floatlab/floatlab-core/pkg/store"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

var backupSourceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var backupWorkloadDataset = regexp.MustCompile(`^floatlab/[a-z0-9]+(?:-[a-z0-9]+)*/[A-Za-z0-9_.-]+$`)

type backupRestoreRequest struct {
	Name              string                         `json:"name"`
	SourceNodeID      string                         `json:"source_node_id"`
	BackupNodeID      string                         `json:"backup_node_id"`
	DestinationNodeID string                         `json:"destination_node_id"`
	ComposeFile       string                         `json:"compose_file"`
	Snapshots         []worker.BackupRestoreSnapshot `json:"snapshots"`
}

func (s *Server) backupSnapshots(ctx context.Context, node, source, dataset string) ([]ipc.SnapshotInfoResult, error) {
	if !backupSourceID.MatchString(source) || !backupWorkloadDataset.MatchString(dataset) || strings.HasSuffix(dataset, "/..") || strings.HasSuffix(dataset, "/.") {
		return nil, fmt.Errorf("invalid backup source or workload dataset")
	}
	if _, err := s.store.GetNode(ctx, node); err != nil {
		return nil, fmt.Errorf("unknown backup node")
	}
	if s.cfg != nil && s.cfg.LocalNodeID != "" && node != s.cfg.LocalNodeID {
		var result []ipc.SnapshotInfoResult
		err := s.backupPeerRequest(ctx, node, http.MethodGet, "/internal/backups/snapshots?"+url.Values{"source_node_id": {source}, "dataset": {dataset}}.Encode(), nil, &result)
		return result, err
	}
	raw, err := s.hosts.Execute(ctx, node, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: store.BackupDataset(source, dataset)})
	if err != nil {
		return nil, err
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	if result.Snapshots == nil {
		result.Snapshots = []ipc.SnapshotInfoResult{}
	}
	return result.Snapshots, nil
}

func (s *Server) handleListBackupSnapshots(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	snaps, err := s.backupSnapshots(r.Context(), q.Get("node_id"), q.Get("source_node_id"), q.Get("dataset"))
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, snaps)
}

// Recovery uses the last published configuration replica when its owner is offline.
func (s *Server) handleBackupConfig(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("stack_id")
	if id == "" {
		writeError(w, 400, "stack_id required")
		return
	}
	stack, _ := s.store.GetStack(r.Context(), id)
	err := s.eachReplica(r.Context(), func(db *rqlite.Client, _ rqlite.LocalIdentity, _ time.Time) error {
		candidate, err := config.NewStore(db).GetStack(r.Context(), id)
		if err != nil {
			return nil
		}
		if stack != nil {
			return fmt.Errorf("conflicting workload ownership requires reconciliation")
		}
		stack = candidate
		return nil
	})
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	if stack == nil {
		writeError(w, 404, "saved stack configuration not found; supply the original Compose configuration")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(stack.ComposeYAML))
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request) {
	var req backupRestoreRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, 400, "invalid JSON")
		return
	}
	name, err := compose.Slug(req.Name)
	if err != nil || !backupSourceID.MatchString(req.SourceNodeID) || name == "system" || name == "backups" || name == "restores" {
		writeError(w, 400, "valid name and source_node_id required")
		return
	}
	for _, id := range []string{req.BackupNodeID, req.DestinationNodeID} {
		if _, err := s.store.GetNode(r.Context(), id); err != nil {
			writeError(w, 400, "unknown backup or destination node")
			return
		}
	}
	if s.cfg != nil && s.cfg.LocalNodeID != "" && req.DestinationNodeID != s.cfg.LocalNodeID {
		writeError(w, 409, "restore must be accepted by its destination node")
		return
	}
	var document struct {
		Name string `yaml:"name"`
	}
	if err := yaml.Unmarshal([]byte(req.ComposeFile), &document); err != nil || document.Name == "" {
		writeError(w, 400, "compose_file must contain the original stack name")
		return
	}
	restored, err := compose.RestoreSource(req.ComposeFile, document.Name, name, req.DestinationNodeID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	source, err := compose.ParseLifecycle(req.ComposeFile, document.Name)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if len(req.Snapshots) == 0 || len(req.Snapshots) != len(source.Mounts) {
		writeError(w, 400, "a backup snapshot is required for every writable mount")
		return
	}
	seen := map[string]bool{}
	for _, selected := range req.Snapshots {
		if _, found := source.Mounts[selected.Dataset]; !found || seen[selected.Dataset] || !store.IsAutomatedSnapshot(selected.Snapshot) {
			writeError(w, 400, "invalid, duplicate, or unmanaged backup snapshot")
			return
		}
		seen[selected.Dataset] = true
		snaps, err := s.backupSnapshots(r.Context(), req.BackupNodeID, req.SourceNodeID, selected.Dataset)
		if err != nil {
			writeError(w, 502, err.Error())
			return
		}
		found := false
		for _, snap := range snaps {
			if snap.Name == selected.Snapshot {
				found = true
				break
			}
		}
		if !found {
			writeError(w, 400, "selected backup snapshot is missing: "+selected.Dataset)
			return
		}
	}
	root := "floatlab/" + name
	raw, err := s.hosts.Execute(r.Context(), req.DestinationNodeID, "fs.dataset.list", ipc.DatasetListPayload{Parent: "floatlab"})
	if err != nil {
		writeError(w, 502, err.Error())
		return
	}
	var datasets ipc.DatasetListResult
	if err := json.Unmarshal(raw, &datasets); err != nil {
		writeError(w, 502, err.Error())
		return
	}
	for _, dataset := range datasets.Datasets {
		if dataset.Name == root || strings.HasPrefix(dataset.Name, root+"/") {
			writeError(w, 409, "restore destination dataset already exists")
			return
		}
	}
	sort.Slice(req.Snapshots, func(i, j int) bool { return req.Snapshots[i].Dataset < req.Snapshots[j].Dataset })
	stack := &config.Stack{ID: uuid.NewString(), Name: name, PrimaryNodeID: req.DestinationNodeID, ComposeYAML: restored, ZFSDataset: root, FailoverMode: "manual"}
	if err := s.store.CreateStack(r.Context(), stack); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	opID := operationID(r.Context())
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `UPDATE operations SET stack_id=?,action='start' WHERE id=?`, Params: []interface{}{stack.ID, opID}}}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if err := s.raft.Apply(run.StackStateChanged{StackID: stack.ID, From: run.StateIdle, To: run.StateRestoring, Event: run.EventBackupRestoreStart, Timestamp: time.Now().UTC(), NodeID: stack.PrimaryNodeID}, 10*time.Second); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	payload := worker.BackupRestorePayload{OperationID: opID, StackID: stack.ID, SourceNodeID: req.SourceNodeID, BackupNodeID: req.BackupNodeID, DestinationNodeID: req.DestinationNodeID, DatasetPath: root, Snapshots: req.Snapshots, Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "backup-restore-"+opID, worker.TaskBackupRestore, stack.ID, payload); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": stack.ID, "status": "pending"})
}
