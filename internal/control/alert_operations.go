package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

const operationFailureRule = "management.operation.failed"

// A rejected or synchronous request is not proof of a terminal lifecycle outcome.
const terminalManagementOperations = `WITH classified AS (
	SELECT o.id,o.stack_id,o.state,o.checkpoint,o.created_at,o.updated_at,COALESCE(s.primary_node_id,'') AS node_id,
		CASE
			WHEN i.path='/api/v1/storage/backups/restore' OR o.action='backup-restore' THEN 'backup-restore'
			WHEN i.path GLOB '/api/v1/failover/*/trigger' OR i.path GLOB '/api/v1/stacks/*/failover' OR o.action IN ('failover','trigger') THEN 'failover'
			WHEN i.path GLOB '/api/v1/stacks/*/snapshots/*/restore' OR o.action='snapshot-restore' THEN 'snapshot-restore'
			WHEN i.path GLOB '/api/v1/storage/datasets/*/rollback' OR o.action='rollback' THEN 'dataset-rollback'
			WHEN o.action IN ('create','start','stop','restart','upgrade','move','delete','restore') THEN o.action
		END AS kind
	FROM operations o LEFT JOIN idempotency_keys i ON i.operation_id=o.id LEFT JOIN stacks s ON s.id=o.stack_id
	WHERE o.state IN ('succeeded','failed') AND o.stack_id<>'' AND (i.operation_id IS NULL OR i.status=202)
), latest AS (
	SELECT *,ROW_NUMBER() OVER (PARTITION BY stack_id,kind ORDER BY julianday(created_at) DESC,created_at DESC,julianday(updated_at) DESC,updated_at DESC,id DESC) AS outcome_rank
	FROM classified WHERE kind IS NOT NULL
)
SELECT id,stack_id,kind,state,checkpoint,created_at,updated_at,node_id FROM latest WHERE outcome_rank=1`

func (s *Server) evaluateOperationFailures(ctx context.Context, now time.Time) error {
	rule, err := s.builtInAlertRule(ctx, operationFailureRule)
	if err != nil {
		return err
	}
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id,state_json FROM monitoring_checks WHERE producer=?`, Params: []any{operationFailureRule}})
	if err != nil {
		return err
	}
	type priorCheck struct {
		monitoring.Check
		encoded string
	}
	checks := make(map[string]priorCheck, len(rows.Values))
	for _, row := range rows.Values {
		check := priorCheck{encoded: str(row[1])}
		if err := json.Unmarshal([]byte(check.encoded), &check.Check); err != nil {
			return err
		}
		checks[str(row[0])] = check
	}
	var passErr error
	if !rule.Enabled {
		for _, check := range checks {
			if check.CheckID == "" || check.Status == "disabled" {
				continue
			}
			result := check.Result
			result.EvaluatedAt, result.Status, result.Reason = now, "disabled", "rule disabled"
			passErr = errors.Join(passErr, s.recordOperationCheck(ctx, result, "retire", result.Reason, check.encoded, false))
		}
		return passErr
	}

	rows, err = s.db.QueryStrong(ctx, rqlite.Statement{SQL: terminalManagementOperations})
	if err != nil {
		return err
	}
	for _, row := range rows.Values {
		id, stackID, kind, state, checkpoint := str(row[0]), str(row[1]), str(row[2]), str(row[3]), operationAlertCheckpoint(str(row[4]))
		started, finished := operationAlertTime(str(row[5])), operationAlertTime(str(row[6]))
		if started.IsZero() || finished.Before(started) || finished.After(now) {
			continue
		}
		checkID := operationFailureRule + ":" + stackID + ":" + kind
		old := checks[checkID]
		previousStart := operationAlertTime(str(old.Evidence["created_at"]))
		previousFinish := operationAlertTime(str(old.Evidence["terminal_at"]))
		if state == "succeeded" && old.IncidentID == "" || started.Before(previousStart) || finished.Before(previousFinish) || finished.Equal(previousFinish) && old.Status != "disabled" {
			continue
		}
		nodeID, observer := str(row[7]), ""
		if s.cfg != nil {
			observer = s.cfg.LocalNodeID
			if nodeID == "" {
				nodeID = observer
			}
		}
		result := monitoring.Result{
			CheckID: checkID, Producer: operationFailureRule, Kind: operationFailureRule, Observer: observer,
			Resource:   monitoring.Resource{HostID: nodeID, WorkloadID: stackID},
			Labels:     map[string]string{"rule_id": operationFailureRule, "stack_id": stackID, "node_id": nodeID, "operation_kind": kind},
			Evidence:   map[string]any{"operation_id": id, "operation_kind": kind, "checkpoint": checkpoint, "created_at": started.Format(time.RFC3339Nano), "terminal_at": finished.Format(time.RFC3339Nano)},
			ObservedAt: now, EvaluatedAt: now,
		}
		action := "open"
		result.Status = "failing"
		message := fmt.Sprintf("Stack operation %s failed. Inspect operation %s for details.", kind, id)
		if state == "succeeded" {
			action, result.Status = "resolve", "healthy"
			message = fmt.Sprintf("Stack operation %s succeeded in operation %s.", kind, id)
		} else if checkpoint != "failed" {
			message = fmt.Sprintf("Stack operation %s failed at checkpoint %s. Inspect operation %s for details.", kind, checkpoint, id)
		}
		result.Reason = message
		passErr = errors.Join(passErr, s.recordOperationCheck(ctx, result, action, message, old.encoded, true))
	}
	return passErr
}

func operationAlertTime(value string) time.Time {
	if stamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return stamp
	}
	return taskTime(value)
}

func (s *Server) recordOperationCheck(ctx context.Context, result monitoring.Result, action, message, previous string, sendNotification bool) error {
	if previous == "" {
		previous = "{}"
	}
	return monitoring.Record(ctx, s.db, s.broker, monitoring.Transition{
		Result: result, RuleID: operationFailureRule, Title: "Management operation failed", Message: message,
		Severity: "critical", Action: action, Notify: sendNotification,
		Guard:       `EXISTS(SELECT 1 FROM monitoring_checks WHERE id=? AND state_json=?)`,
		GuardParams: []any{result.CheckID, previous},
	})
}

func operationAlertCheckpoint(checkpoint string) string {
	switch checkpoint {
	case "activating-snapshot", "compose-down", "network-release", "preserving-current", "provisioning", "pulling-images",
		"purging", "reconciling", "reservation-canceled", "restoring-data", "rollback-failed", "rolled-back",
		"snapshotting", "starting", "stopping", "succeeded", "updating-source":
		return checkpoint
	default:
		return "failed"
	}
}
