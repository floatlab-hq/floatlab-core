package control

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

type stackMoveRequest struct {
	DestinationNodeID string `json:"destination_node_id"`
	NetworkPool       string `json:"network_pool,omitempty"`
}

type stackMove struct {
	OperationID   string                     `json:"operation_id"`
	StackID       string                     `json:"stack_id"`
	Stack         config.Stack               `json:"stack"`
	Destination   string                     `json:"destination"`
	NetworkPool   string                     `json:"network_pool,omitempty"`
	Generation    int64                      `json:"generation"`
	Datasets      []ipc.DatasetCreatePayload `json:"datasets"`
	Targets       []config.BackupTarget      `json:"targets"`
	SeedSnapshot  string                     `json:"seed_snapshot,omitempty"`
	FinalSnapshot string                     `json:"final_snapshot,omitempty"`
}

func (s *Server) handleStackMove(w http.ResponseWriter, r *http.Request) {
	if s.peerWorkloads == nil || s.cfg.LabDB == nil {
		writeError(w, http.StatusConflict, "stack move requires an active lab")
		return
	}
	ctx := r.Context()
	id, opID := chi.URLParam(r, "id"), operationID(ctx)
	var req stackMoveRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err := decoder.Decode(&req); err != nil || decoder.Decode(new(any)) != io.EOF || req.DestinationNodeID == "" {
		writeError(w, 400, "destination_node_id required")
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		writeError(w, 400, "invalid stack ID")
		return
	}
	if req.DestinationNodeID == s.cfg.LocalNodeID {
		writeError(w, 409, "choose a different destination host")
		return
	}
	if _, ok := s.peerWorkloads.lookup(req.DestinationNodeID); !ok {
		writeError(w, 409, "destination must be an active lab member")
		return
	}
	st, err := s.store.GetStack(ctx, id)
	if err != nil {
		writeError(w, 404, "stack not found")
		return
	}
	instance, ok := s.raft.FSM().State(id)
	if st.PrimaryNodeID != s.cfg.LocalNodeID || !ok || instance.State != run.StateRunningPrimary {
		writeError(w, 409, "stack must be running on its primary host")
		return
	}
	other, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT id FROM operations WHERE stack_id=? AND id<>? AND state IN ('pending','running') LIMIT 1`, Params: []any{id, opID}})
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	if len(other.Values) != 0 {
		writeError(w, 409, "another stack operation is active")
		return
	}
	spec, err := compose.ParseLifecycle(st.ComposeYAML, st.Name)
	if err != nil || st.ZFSDataset != "floatlab/"+st.Name || st.Name == "system" || st.Name == "backups" || st.Name == "restores" {
		writeError(w, 409, "move requires a managed FloatLab stack dataset")
		return
	}
	if err := validateMoveProject(spec, st.ZFSDataset); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	datasets := []ipc.DatasetCreatePayload{{Dataset: st.ZFSDataset, BlockSize: "32K", Compression: "lz4"}}
	for _, mount := range spec.Mounts {
		datasets = append(datasets, ipc.DatasetCreatePayload{Dataset: mount.Dataset, BlockSize: mount.RecordSize, Compression: mount.Compression, Quota: mount.Quota})
	}
	sort.Slice(datasets, func(i, j int) bool { return datasets[i].Dataset < datasets[j].Dataset })
	if err := s.checkMoveDatasets(ctx, st.ZFSDataset, datasets); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	targets, err := s.store.BackupTargets(ctx, id)
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	for _, target := range targets {
		if target.CleanupPending {
			writeError(w, 409, "wait for backup target cleanup before moving the stack")
			return
		}
	}
	generation, _, err := stackMoveOwner(ctx, s.db, id)
	if err != nil {
		writeError(w, 503, err.Error())
		return
	}
	if _, err := s.cfg.LabDB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT operation_id FROM stack_move_grants WHERE stack_id=? LIMIT 1`, Params: []any{id}}); err != nil {
		writeError(w, 503, "lab quorum required to move a stack: "+err.Error())
		return
	}
	move := stackMove{OperationID: opID, StackID: id, Stack: *st, Destination: req.DestinationNodeID,
		NetworkPool: req.NetworkPool, Generation: generation + 1, Datasets: datasets, Targets: targets}
	var prepared struct {
		NetworkPool string `json:"network_pool"`
	}
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.backupPeerRequest(probe, move.Destination, http.MethodPost, "/internal/stack-moves/prepare", move, &prepared); err != nil {
		writeError(w, 409, "destination preflight: "+err.Error())
		return
	}
	move.NetworkPool = prepared.NetworkPool
	if err := worker.EnqueueTask(ctx, s.db, "move-"+opID, worker.TaskStackMove, id, move); err != nil {
		writeError(w, 503, err.Error())
		return
	}
	writeJSON(w, 202, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
}

func validateMoveProject(spec *compose.LifecycleSpec, root string) error {
	for _, service := range spec.Project.Services {
		for _, volume := range service.Volumes {
			if volume.ReadOnly && (volume.Type == "volume" || (volume.Type == "bind" && !strings.HasPrefix(volume.Source, "/"+root+"/"))) {
				return fmt.Errorf("move cannot transfer service %s mount %s outside the managed datasets", service.Name, volume.Source)
			}
		}
	}
	for _, network := range spec.Project.Networks {
		if network.External {
			return fmt.Errorf("move cannot transfer external network %s", network.Name)
		}
	}
	return nil
}

func (s *Server) checkMoveDatasets(ctx context.Context, root string, datasets []ipc.DatasetCreatePayload) error {
	raw, err := s.hosts.Execute(ctx, s.cfg.LocalNodeID, "fs.dataset.list", ipc.DatasetListPayload{Parent: root})
	if err != nil {
		return err
	}
	var result ipc.DatasetListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return err
	}
	expected := make(map[string]bool, len(datasets))
	for _, dataset := range datasets {
		expected[dataset.Dataset] = true
	}
	for _, dataset := range result.Datasets {
		if !expected[dataset.Name] {
			return fmt.Errorf("cannot move unmanaged dataset %s", dataset.Name)
		}
		delete(expected, dataset.Name)
	}
	if len(expected) != 0 {
		return fmt.Errorf("one or more managed stack datasets are missing")
	}
	return nil
}

func (s *Server) moveNetworkPool(ctx context.Context, node, requested, current string) (string, error) {
	pools, err := ipam.ListPools(ctx, s.db)
	if err != nil {
		return "", err
	}
	host, err := ipam.HostStatus(ctx, s.hosts, node)
	if err != nil {
		return "", err
	}
	selected, err := ipam.SelectPool(pools, requested, host.Config.DefaultPoolID, node)
	if requested == "" && err != nil {
		selected, err = ipam.SelectPool(pools, current, "", node)
		if err != nil {
			for i := range pools {
				if ipam.Member(pools[i], node) {
					if selected != nil {
						return "", fmt.Errorf("select a network_pool for destination %s", node)
					}
					selected = &pools[i]
				}
			}
		}
	}
	if selected == nil {
		return "", err
	}
	if err := ipam.Eligible(ctx, s.hosts, *selected, node); err != nil {
		return "", err
	}
	return selected.Name, nil
}

func movedCompose(source, destination, original, pool string) (string, error) {
	var project map[string]any
	if err := yaml.Unmarshal([]byte(source), &project); err != nil {
		return "", err
	}
	extension, ok := project["x-fl-stack"].(map[string]any)
	if project == nil || (!ok && project["x-fl-stack"] != nil) {
		return "", fmt.Errorf("invalid x-fl-stack")
	}
	if extension == nil {
		extension = map[string]any{"schema_version": 1}
		project["x-fl-stack"] = extension
	}
	extension["primary_node"] = destination
	extension["secondary_node"] = original
	if pool != "" {
		project["x-fl-network-pool"] = pool
	}
	reverseComposeTargets(project, destination, original)
	raw, err := yaml.Marshal(project)
	return string(raw), err
}

func reverseComposeTargets(value any, destination, original string) {
	switch node := value.(type) {
	case map[string]any:
		if targets, ok := node["x-fl-backups"].([]any); ok {
			for _, target := range targets {
				if policy, ok := target.(map[string]any); ok && policy["node_id"] == destination {
					policy["node_id"] = original
				}
			}
		}
		for _, child := range node {
			reverseComposeTargets(child, destination, original)
		}
	case []any:
		for _, child := range node {
			reverseComposeTargets(child, destination, original)
		}
	}
}

func (s *Server) applyMoveState(id string, from, to run.State, event run.Event) error {
	return s.raft.Apply(run.StackStateChanged{StackID: id, From: from, To: to, Event: event, NodeID: s.cfg.LocalNodeID, Timestamp: time.Now().UTC()}, 10*time.Second)
}
