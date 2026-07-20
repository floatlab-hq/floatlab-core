package control

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

func (s *Server) handleStackConfig(w http.ResponseWriter, r *http.Request) {
	stack, err := s.store.GetStack(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write([]byte(stack.ComposeYAML))
}

func (s *Server) handleStackStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	state := ""
	nodeID := stack.PrimaryNodeID
	if instance, ok := s.raft.FSM().State(id); ok {
		state = string(instance.State)
		if instance.State == run.StateRunningBackup {
			nodeID = stack.BackupNodeID
		}
	}
	var containers []ipc.ContainerInfo
	if raw, err := s.hosts.Execute(r.Context(), nodeID, "docker.list", ipc.DockerListPayload{StackID: id}); err == nil {
		var result ipc.DockerListResult
		if json.Unmarshal(raw, &result) == nil {
			containers = result.Containers
		}
	}
	var active interface{}
	if operations, err := s.ops.Active(r.Context()); err == nil {
		for _, candidate := range operations {
			if candidate.StackID == id {
				active = candidate
				break
			}
		}
	}
	stackIP := ""
	if result, err := s.db.Query(r.Context(), rqlite.Statement{SQL: `SELECT stack_ip FROM stack_runtime WHERE stack_id=?`, Params: []interface{}{id}}); err == nil && len(result.Values) > 0 {
		stackIP, _ = result.Values[0][0].(string)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"stack_id": id, "state": state, "stack_ip": stackIP, "active_operation": active, "containers": containers})
}

func (s *Server) handleStackEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := operation.ListEvents(r.Context(), s.db, chi.URLParam(r, "id"), r.URL.Query().Get("after"), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	next := ""
	if len(events) > 0 {
		next = events[len(events)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"items": events, "next_cursor": next})
}

func (s *Server) handleStackAlerts(w http.ResponseWriter, r *http.Request) {
	result, err := s.db.Query(r.Context(), rqlite.Statement{
		SQL: `SELECT r.id,r.name,r.metric,r.selector,r.comparator,r.threshold,r.duration,r.severity,r.message,r.active,s.state,s.observed,s.observed_at
		      FROM stack_alert_rules r LEFT JOIN alert_status s ON s.rule_id=r.id WHERE r.stack_id=? ORDER BY r.name`,
		Params: []interface{}{chi.URLParam(r, "id")},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"rows": result.Values})
}

func (s *Server) handleStackSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	raw, err := s.hosts.Execute(r.Context(), stack.PrimaryNodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: stack.ZFSDataset})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result.Snapshots)
}

func (s *Server) handleCreateStackSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	opID := operationID(r.Context())
	name := "snapshot-" + opID
	now := time.Now().UTC()
	if err := s.db.Execute(r.Context(), []rqlite.Statement{
		{SQL: `INSERT INTO stack_snapshots(id,stack_id,operation_id,zfs_name,kind,created_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{uuid.NewString(), id, opID, name, "user", now}},
		{SQL: `INSERT INTO recovery_points(id,stack_id,snapshot_id,dataset_path,compose_yaml,created_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{uuid.NewString(), id, name, stack.ZFSDataset, stack.ComposeYAML, now}},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	payload := worker.SnapshotCreatePayload{Dataset: stack.ZFSDataset, NodeID: stack.PrimaryNodeID, SnapType: "user", Recursive: true, OperationID: opID, StackID: id, Name: name, Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "snapshot-"+opID, worker.TaskSnapshotCreate, id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending", "snapshot": name})
}

func (s *Server) handleDeleteStackSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	stack, err := s.store.GetStack(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	opID := operationID(r.Context())
	snapshotID := chi.URLParam(r, "snapshotId")
	recoveryDataset := ""
	if result, err := s.db.Query(r.Context(), rqlite.Statement{SQL: `SELECT dataset_path FROM recovery_points WHERE stack_id=? AND (id=? OR snapshot_id=?) AND deleted_at IS NULL`, Params: []interface{}{id, snapshotID, snapshotID}}); err == nil && len(result.Values) > 0 {
		candidate, _ := result.Values[0][0].(string)
		if strings.Contains(candidate, "-recovery-") {
			recoveryDataset = candidate
		}
	}
	payload := worker.SnapshotDeletePayload{Dataset: stack.ZFSDataset, NodeID: stack.PrimaryNodeID, Snapshot: snapshotID, OperationID: opID, StackID: id, RecoveryDataset: recoveryDataset, Actor: actor(r)}
	if err := worker.EnqueueTask(r.Context(), s.db, "snapshot-delete-"+opID, worker.TaskSnapshotDelete, id, payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `UPDATE recovery_points SET deleted_at=? WHERE stack_id=? AND snapshot_id=?`, Params: []interface{}{time.Now().UTC(), id, chi.URLParam(r, "snapshotId")}}})
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID, "stack_id": id, "status": "pending"})
}

func (s *Server) handleAlertTransition(w http.ResponseWriter, r *http.Request) {
	var request struct {
		RuleID     string    `json:"rule_id"`
		State      string    `json:"state"`
		Observed   float64   `json:"observed"`
		ObservedAt time.Time `json:"observed_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || (request.State != "pending" && request.State != "firing" && request.State != "resolved") {
		writeError(w, http.StatusBadRequest, "invalid alert transition")
		return
	}
	if request.ObservedAt.IsZero() {
		request.ObservedAt = time.Now().UTC()
	}
	eventID, now := uuid.NewString(), time.Now().UTC()
	details, _ := json.Marshal(map[string]interface{}{"rule_id": request.RuleID, "state": request.State, "observed": request.Observed})
	if err := s.db.Execute(r.Context(), []rqlite.Statement{
		{SQL: `INSERT INTO alert_status(rule_id,state,observed,observed_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(rule_id) DO UPDATE SET state=excluded.state,observed=excluded.observed,observed_at=excluded.observed_at,updated_at=excluded.updated_at`, Params: []interface{}{request.RuleID, request.State, request.Observed, request.ObservedAt, now}},
		{SQL: `INSERT INTO lifecycle_events(id,occurred_at,stack_id,type,outcome,actor,details) SELECT ?,?,stack_id,'Alert',? ,?,? FROM stack_alert_rules WHERE id=?`, Params: []interface{}{eventID, now, request.State, actor(r), string(details), request.RuleID}},
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": request.State})
}

func actor(r *http.Request) string {
	value, _ := r.Context().Value(keyActor).(string)
	if value == "" {
		return "system"
	}
	return value
}
