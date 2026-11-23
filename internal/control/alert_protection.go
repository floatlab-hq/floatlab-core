package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

const protectionObserver = "scheduled-protection"

func (s *Server) evaluateProtection(ctx context.Context, now time.Time) error {
	replication, err := s.builtInAlertRule(ctx, "replication.failed")
	if err != nil {
		return err
	}
	snapshot, err := s.builtInAlertRule(ctx, "snapshot.schedule.missed")
	if err != nil {
		return err
	}
	age, err := s.builtInAlertRule(ctx, "replication.recovery_point.stale")
	if err != nil {
		return err
	}
	needed := max(1, int(replication.Thresholds["consecutive_failures"]))
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `WITH ranked AS (
		SELECT e.record_json,e.state,t.state AS task_state,t.started_at,t.updated_at,
		       ROW_NUMBER() OVER (PARTITION BY json_extract(e.record_json,'$.kind'),json_extract(e.record_json,'$.resource'),json_extract(e.record_json,'$.generation') ORDER BY json_extract(e.record_json,'$.scheduled_at') DESC) AS position,
		       MIN(json_extract(e.record_json,'$.scheduled_at')) OVER (PARTITION BY json_extract(e.record_json,'$.kind'),json_extract(e.record_json,'$.resource'),json_extract(e.record_json,'$.generation')) AS first_due
		FROM monitoring_executions e LEFT JOIN tasks t ON t.id=e.id
		WHERE json_extract(e.record_json,'$.kind') IN ('snapshot','replication'))
		SELECT record_json,task_state,started_at,updated_at,first_due FROM ranked
		WHERE position<=? OR (state NOT IN ('succeeded','failed') AND task_state IS NOT NULL) ORDER BY position`, Params: []any{needed + 2}})
	if err != nil {
		return err
	}
	targets, err := s.store.BackupTargets(ctx, "")
	if err != nil {
		return err
	}
	checks, err := s.protectionChecks(ctx)
	if err != nil {
		return err
	}
	active := make(map[monitoring.Resource]config.BackupTarget, len(targets))
	for _, target := range targets {
		if target.Enabled && target.Schedule != "" {
			active[monitoring.Resource{HostID: target.SourceNodeID, WorkloadID: target.StackID, Dataset: target.Dataset, SourceID: target.SourceNodeID, DestinationID: target.NodeID}] = target
		}
	}
	type history struct {
		firstDue time.Time
		runs     []monitoring.Execution
	}
	histories := make(map[monitoring.Resource]*history)
	var passErr error
	for _, row := range rows.Values {
		var run monitoring.Execution
		if err := json.Unmarshal([]byte(str(row[0])), &run); err != nil {
			return err
		}
		rule := snapshot
		if run.Kind == "replication" {
			rule = replication
			if target, ok := active[run.Resource]; !ok || target.Generation != run.Generation {
				continue
			}
		}
		grace, budget := protectionSeconds(rule.Thresholds["start_grace_seconds"]), protectionSeconds(rule.Thresholds["completion_budget_seconds"])
		if policy := active[run.Resource].MonitoringPolicy; policy != nil {
			grace = protectionOverride(grace, policy.StartGraceSeconds)
			budget = protectionOverride(budget, policy.CompletionBudgetSeconds)
		}
		updated := reconcileProtectionExecution(run, str(row[1]), taskTime(str(row[2])), taskTime(str(row[3])), now, grace, budget)
		if updated.State != run.State || updated.WasMissed != run.WasMissed || updated.WasOverdue != run.WasOverdue {
			if err := monitoring.SaveExecution(ctx, s.db, updated); err != nil {
				passErr = errors.Join(passErr, err)
				continue
			}
		}
		if histories[run.Resource] == nil {
			firstDue, _ := time.Parse(time.RFC3339, str(row[4]))
			histories[run.Resource] = &history{firstDue: firstDue}
		}
		histories[run.Resource].runs = append(histories[run.Resource].runs, updated)
	}
	seen := make(map[string]bool)
	for resource, h := range histories {
		ruleID, title, limit, enabled := "snapshot.schedule.missed", "Scheduled snapshot", 1, snapshot.Enabled
		if resource.DestinationID != "" {
			ruleID, title, limit, enabled = "replication.failed", "Scheduled replication", needed, replication.Enabled
		} else {
			configured, err := s.hasSnapshotSchedule(ctx, resource)
			if err != nil {
				return errors.Join(passErr, err)
			}
			enabled = enabled && configured
		}
		if !enabled {
			continue
		}
		transition := protectionTransition(ruleID, title, resource, now)
		seen[transition.Result.CheckID] = true
		outcome, failures := protectionOutcome(h.runs)
		if outcome.ID == "" {
			continue
		}
		transition.Result.Evidence = map[string]any{"execution_id": outcome.ID, "scheduled_at": outcome.ScheduledAt.Format(time.RFC3339), "consecutive_failures": failures}
		if len(h.runs) > 0 && h.runs[0].ScheduledAt.After(outcome.ScheduledAt) {
			transition.Result.Evidence["next_scheduled_at"] = h.runs[0].ScheduledAt.Format(time.RFC3339)
		}
		transition.Result.ObservedAt = now
		if outcome.CompletedAt != nil {
			transition.Result.Evidence["completed_at"] = outcome.CompletedAt.Format(time.RFC3339)
		}
		if failures == 0 {
			transition.Result.Status, transition.Action, transition.Notify = "healthy", "resolve", true
			transition.Message = "Scheduled execution succeeded"
		} else {
			transition.Result.Status, transition.Action, transition.Notify = "failing", "open", failures >= limit
			transition.Message = outcome.Reason
		}
		transition.Result.Reason = transition.Message
		if old := checks[transition.Result.CheckID]; old.Evidence["execution_id"] != outcome.ID || number(old.Evidence["consecutive_failures"]) != failures || old.Evidence["next_scheduled_at"] != transition.Result.Evidence["next_scheduled_at"] || old.Status != transition.Result.Status || old.Reason != transition.Message || (transition.Notify && failures != 0 && !old.Notified) {
			passErr = errors.Join(passErr, s.recordProtection(ctx, transition))
		}
	}
	for resource, target := range active {
		warning, maximum := protectionSeconds(age.Thresholds["warning_age_seconds"]), protectionSeconds(age.Thresholds["max_age_seconds"])
		if policy := target.MonitoringPolicy; policy != nil {
			warning = protectionOverride(warning, policy.WarningAgeSeconds)
			maximum = protectionOverride(maximum, policy.MaxAgeSeconds)
		}
		if !age.Enabled || (warning <= 0 && maximum <= 0) {
			continue
		}
		transition := protectionTransition("replication.recovery_point.stale", "Replication recovery point", resource, now)
		seen[transition.Result.CheckID] = true
		old := checks[transition.Result.CheckID]
		if warning <= 0 || maximum <= warning {
			transition.Result.Status, transition.Action = "unknown", "keep"
			transition.Result.Reason = "Configure a positive warning age below the maximum recovery point age"
			if old.Status != transition.Result.Status || old.Reason != transition.Result.Reason {
				passErr = errors.Join(passErr, s.recordProtection(ctx, transition))
			}
			continue
		}
		var initialDeadline time.Time
		if h := histories[resource]; h != nil {
			budget := protectionSeconds(replication.Thresholds["completion_budget_seconds"])
			if policy := target.MonitoringPolicy; policy != nil {
				budget = protectionOverride(budget, policy.CompletionBudgetSeconds)
			}
			if budget > 0 {
				initialDeadline = h.firstDue.Add(budget)
			}
		}
		if err := s.recoveryPointTransition(ctx, &transition, target, warning, maximum, initialDeadline, now); err != nil {
			passErr = errors.Join(passErr, err)
			continue
		}
		if old.Status != transition.Result.Status || old.Reason != transition.Message || (transition.Action == "open" && old.Severity != transition.Severity) || old.Evidence["snapshot"] != transition.Result.Evidence["snapshot"] {
			passErr = errors.Join(passErr, s.recordProtection(ctx, transition))
		}
	}
	for id, check := range checks {
		if seen[id] || check.Status == "disabled" {
			continue
		}
		transition := protectionTransition(check.Kind, check.Kind, check.Resource, now)
		transition.Result.Status, transition.Action = "disabled", "retire"
		transition.Result.Reason = "rule or protection policy disabled or removed"
		passErr = errors.Join(passErr, s.recordProtection(ctx, transition))
	}
	return passErr
}

func reconcileProtectionExecution(run monitoring.Execution, state string, started, updated, now time.Time, grace, budget time.Duration) monitoring.Execution {
	if run.State == "succeeded" || run.State == "failed" {
		return run
	}
	if started.IsZero() {
		if !now.Before(run.ScheduledAt.Add(grace)) {
			run.State, run.WasMissed, run.Reason = "missed", true, "Scheduled execution did not start by its deadline"
		}
		return run
	}
	run.StartedAt = &started
	run.WasMissed = run.WasMissed || started.After(run.ScheduledAt.Add(grace))
	if state == "done" || state == "failed" {
		run.State, run.Active, run.CompletedAt, run.Reason = "succeeded", false, &updated, ""
		if state == "failed" {
			run.State, run.Reason = "failed", "Scheduled execution failed"
		}
		run.WasOverdue = run.WasOverdue || (budget > 0 && updated.After(run.ScheduledAt.Add(budget)))
		return run
	}
	run.State, run.Active, run.Reason = "running", true, ""
	if run.WasOverdue || (budget > 0 && !now.Before(run.ScheduledAt.Add(budget))) {
		run.State, run.WasOverdue, run.Reason = "overdue", true, "Scheduled execution did not complete by its deadline"
	} else if run.WasMissed {
		run.Reason = "Scheduled execution started after its deadline"
	}
	return run
}

func protectionOutcome(runs []monitoring.Execution) (monitoring.Execution, int) {
	var latest monitoring.Execution
	failures := 0
	for _, run := range runs {
		if run.State == "succeeded" {
			if latest.ID == "" {
				latest = run
			}
			break
		}
		if run.State == "failed" || run.State == "missed" || run.State == "overdue" || run.WasMissed {
			if latest.ID == "" {
				latest = run
			}
			failures++
		} else if latest.ID != "" {
			break
		}
	}
	return latest, failures
}

func (s *Server) hasSnapshotSchedule(ctx context.Context, resource monitoring.Resource) (bool, error) {
	stack, err := s.store.CatalogStack(ctx, resource.WorkloadID)
	if errors.Is(err, config.ErrStackNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if stack.PrimaryNodeID != resource.HostID {
		return false, nil
	}
	source, err := s.store.ReadStackCompose(ctx, stack.ID)
	if err != nil {
		return false, err
	}
	spec, err := compose.ParseLifecycle(source, stack.Name)
	if err == nil {
		return len(spec.Mounts[resource.Dataset].Snapshots) > 0, nil
	}
	legacy, err := compose.Parse(ctx, source, stack.Name)
	if err != nil || resource.Dataset != stack.ZFSDataset {
		return false, nil
	}
	schedule := legacy.Extension.Snapshots.Schedule
	return schedule.Hourly.Keep > 0 || schedule.Daily.Keep > 0 || schedule.Weekly.Keep > 0 || schedule.Monthly.Keep > 0, nil
}

func (s *Server) recoveryPointTransition(ctx context.Context, t *monitoring.Transition, target config.BackupTarget, warning, maxAge time.Duration, initialDeadline, now time.Time) error {
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{
		SQL:    `SELECT snapshot,guid,snapshot_time FROM monitoring_recovery_points WHERE stack_id=? AND dataset=? AND source_id=? AND destination_id=? AND generation=?`,
		Params: []any{target.StackID, target.Dataset, target.SourceNodeID, target.NodeID, target.Generation},
	})
	if err != nil {
		return err
	}
	t.Result.Status, t.Action = "unknown", "keep"
	t.Message = "Awaiting a verified destination recovery point"
	if len(rows.Values) > 0 {
		point := rows.Values[0]
		created := time.Unix(int64(number(point[2])), 0).UTC()
		if created.After(now) {
			t.Message = "Recovery point timestamp is in the future"
		} else {
			age := now.Sub(created)
			t.Result.ObservedAt = now
			t.Result.Evidence = map[string]any{"snapshot": point[0], "guid": point[1], "snapshot_time": created.Format(time.RFC3339), "warning_age_seconds": warning.Seconds(), "max_age_seconds": maxAge.Seconds()}
			t.Result.Status, t.Action, t.Notify, t.Message = "healthy", "resolve", true, "Verified destination recovery point is within its warning age"
			if age > warning {
				t.Result.Status, t.Action, t.Message = "failing", "open", "Verified destination recovery point exceeds its warning age"
			}
			if age > maxAge {
				t.Severity, t.Message = "critical", "Verified destination recovery point exceeds its maximum age"
			}
		}
	} else if !initialDeadline.IsZero() && !now.Before(initialDeadline) {
		t.Result.Status, t.Action, t.Notify, t.Severity = "failing", "open", true, "critical"
		t.Result.ObservedAt, t.Message = now, "No verified destination recovery point by the initial completion deadline"
	}
	t.Result.Reason = t.Message
	return nil
}

func protectionSeconds(value float64) time.Duration {
	return time.Duration(value * float64(time.Second))
}

func protectionOverride(inherited time.Duration, seconds int64) time.Duration {
	if seconds == 0 {
		return inherited
	}
	return time.Duration(seconds) * time.Second
}

func taskTime(value string) time.Time {
	t, _ := time.Parse(time.DateTime, value)
	return t
}

func protectionTransition(rule, title string, resource monitoring.Resource, now time.Time) monitoring.Transition {
	identity, _ := json.Marshal(resource)
	labels := map[string]string{"rule_id": rule, "stack_id": resource.WorkloadID, "node_id": resource.HostID, "dataset": resource.Dataset, "source_node_id": resource.SourceID}
	if resource.DestinationID != "" {
		labels["destination_node_id"] = resource.DestinationID
	}
	return monitoring.Transition{Result: monitoring.Result{CheckID: rule + ":" + string(identity), Producer: protectionObserver, Kind: rule, Resource: resource, Labels: labels, EvaluatedAt: now}, RuleID: rule, Title: title, Severity: "warning"}
}

func (s *Server) protectionChecks(ctx context.Context) (map[string]monitoring.Check, error) {
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT state_json FROM monitoring_checks WHERE producer=?`, Params: []any{protectionObserver}})
	if err != nil {
		return nil, err
	}
	checks := make(map[string]monitoring.Check, len(rows.Values))
	for _, row := range rows.Values {
		var check monitoring.Check
		if err := json.Unmarshal([]byte(str(row[0])), &check); err != nil {
			return nil, err
		}
		if check.CheckID != "" {
			checks[check.CheckID] = check
		}
	}
	return checks, nil
}

func (s *Server) recordProtection(ctx context.Context, t monitoring.Transition) error {
	if err := monitoring.Record(ctx, s.db, s.broker, t); err != nil {
		return fmt.Errorf("%s: %w", t.Result.CheckID, err)
	}
	return nil
}
