package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const DefaultWorkloadUpgradeWindow = "02:30-03:30"

type workloadUpgradeSettings struct {
	Window string `json:"window"`
}

func (s *Server) workloadUpgradeSettings(ctx context.Context) (workloadUpgradeSettings, error) {
	settings := workloadUpgradeSettings{Window: DefaultWorkloadUpgradeWindow}
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT window FROM workload_upgrade_settings WHERE id=1`})
	if err == nil && len(rows.Values) != 0 {
		settings.Window = fmt.Sprint(rows.Values[0][0])
	}
	return settings, err
}

func (s *Server) handleGetWorkloadUpgradeSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.workloadUpgradeSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleSetWorkloadUpgradeSettings(w http.ResponseWriter, r *http.Request) {
	var settings workloadUpgradeSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, "invalid upgrade settings")
		return
	}
	if _, err := ParseWorkloadUpgradeWindow(settings.Window); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `INSERT INTO workload_upgrade_settings(id,window) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET window=excluded.window`, Params: []any{settings.Window}}}); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

type WorkloadUpgradeWindow struct{ start, length int }

func ParseWorkloadUpgradeWindow(value string) (WorkloadUpgradeWindow, error) {
	start, end, _ := strings.Cut(value, "-")
	from, fromErr := time.Parse("15:04", start)
	to, toErr := time.Parse("15:04", end)
	if len(value) != 11 || fromErr != nil || toErr != nil || from.Equal(to) {
		return WorkloadUpgradeWindow{}, fmt.Errorf("workload upgrade window must be a nonempty HH:MM-HH:MM range")
	}
	first := from.Hour()*60 + from.Minute()
	return WorkloadUpgradeWindow{start: first, length: (to.Hour()*60 + to.Minute() - first + 1440) % 1440}, nil
}

func (w WorkloadUpgradeWindow) slot(now time.Time) string {
	minute := now.Hour()*60 + now.Minute()
	if (minute-w.start+1440)%1440 >= w.length {
		return ""
	}
	if minute < w.start {
		now = now.AddDate(0, 0, -1)
	}
	return now.Format(time.DateOnly)
}

func (w WorkloadUpgradeWindow) deadline(now time.Time, slot string) time.Time {
	for next := now.Truncate(time.Minute).Add(time.Minute); ; next = next.Add(time.Minute) {
		if w.slot(next) != slot {
			return next
		}
	}
}

func (s *Server) RunWorkloadUpgrades(ctx context.Context, timezone *time.Location) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	check := func(local time.Time, warn bool) {
		settings, err := s.workloadUpgradeSettings(ctx)
		if err != nil {
			s.log.Warn("could not read workload upgrade window", zap.Error(err))
			return
		}
		window, err := ParseWorkloadUpgradeWindow(settings.Window)
		if err != nil {
			s.log.Warn("invalid workload upgrade window", zap.Error(err))
			return
		}
		s.checkWorkloadUpgrades(ctx, window, local, warn)
	}
	check(time.Now().In(timezone), true)
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			local := tick.In(timezone)
			check(local, local.Minute()%5 == 0)
		}
	}
}

func (s *Server) checkWorkloadUpgrades(ctx context.Context, window WorkloadUpgradeWindow, now time.Time, warn bool) {
	slot := window.slot(now)
	if slot == "" && !warn {
		return
	}
	stacks, err := s.store.ListStackCatalog(ctx)
	if err != nil {
		s.log.Warn("could not list workloads for image updates", zap.Error(err))
		return
	}
	var deadline time.Time
	if slot != "" {
		deadline = window.deadline(now, slot)
	}
	for _, stack := range stacks {
		if stack.PrimaryNodeID != s.cfg.LocalNodeID || ctx.Err() != nil {
			continue
		}
		if stack.ComposeFile {
			current, err := s.store.GetStack(ctx, stack.ID)
			if err != nil {
				s.log.Warn("could not read Compose for image updates", zap.String("stack", stack.ID), zap.Error(err))
				continue
			}
			stack = current
		}
		if err := s.checkStackImageUpgrades(ctx, stack, slot, deadline, warn); err != nil {
			s.log.Warn("workload image check failed", zap.String("stack", stack.ID), zap.Error(err))
		}
	}
}

func (s *Server) checkStackImageUpgrades(ctx context.Context, stack *config.Stack, slot string, deadline time.Time, warn bool) error {
	local, err := s.IsLocalWorkloadOwner(ctx, stack.ID)
	if err != nil || !local {
		return err
	}
	instance, ok := s.raft.FSM().State(stack.ID)
	if !ok {
		return nil
	}
	if instance.State == run.StateIdle && warn {
		return s.syncImageAlerts(ctx, stack, stack.PrimaryNodeID, "unversioned-image", "Workload uses a non-versioned image tag", "warning", nil, true)
	}
	if instance.State != run.StateRunningPrimary && instance.State != run.StateRunningBackup {
		return nil
	}
	due := slot != "" && time.Now().Before(deadline)
	if due {
		rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT 1 FROM scheduler_state WHERE stack_id=? AND tier='image-upgrade' AND last_run_at=?`, Params: []any{stack.ID, slot}})
		if err != nil {
			return err
		}
		due = len(rows.Values) == 0
	}
	if !due && !warn {
		return nil
	}
	node := stackNode(stack, s.raft.FSM().State)
	runtime, err := s.runtimeCompose(ctx, stack.ID, stack.Name, stack.ComposeYAML)
	if err != nil {
		return err
	}
	request := ipc.InspectStackImagesPayload{ComposeUpPayload: ipc.ComposeUpPayload{StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: runtime}, Source: stack.ComposeYAML}
	inspectCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	raw, err := s.hosts.Execute(inspectCtx, node, "compose.running-images", request)
	cancel()
	if err != nil {
		return err
	}
	var running ipc.ComposeRunningImages
	if err := json.Unmarshal(raw, &running); err != nil {
		return err
	}
	if warn {
		messages := map[string]string{}
		for service, image := range running.Images {
			if ref, err := compose.ParseImageReference(image.Reference); err == nil && ref.Tag != "" && compose.ParseImageVersion(ref.Tag).Parts < 3 {
				messages[service] = fmt.Sprintf("%s uses the non-versioned tag %s:%s.", service, ref.Repository, ref.Tag)
			}
		}
		if err := s.syncImageAlerts(ctx, stack, node, "unversioned-image", "Workload uses a non-versioned image tag", "warning", messages, true); err != nil {
			return err
		}
	}
	if !due {
		return nil
	}
	return s.planStackUpgrade(ctx, stack, instance, node, running, slot, deadline)
}

func (s *Server) planStackUpgrade(ctx context.Context, stack *config.Stack, instance *run.StackInstance, node string, running ipc.ComposeRunningImages, slot string, deadline time.Time) error {
	spec, err := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
	if err != nil {
		return err
	}
	editable := compose.EditableImages(stack.ComposeYAML)
	candidates, oldPins, alerts := map[string]string{}, map[string]string{}, map[string]string{}
	canApply := running.Matches && instance.State == run.StateRunningPrimary && len(running.Images) > 0
	var lookupErr error
	for name, image := range running.Images {
		service, configured := spec.Project.Services[name]
		if !configured || service.Build != nil || service.Image == "" {
			continue
		}
		ref, err := compose.ParseImageReference(image.Reference)
		if err != nil || len(image.Digests) == 0 {
			canApply = false
			continue
		}
		policy := compose.UpgradePolicy(service)
		desired, err := compose.ParseImageReference(service.Image)
		if err != nil {
			canApply = false
			continue
		}
		lookupReference := image.Reference
		if desired.Digest != "" && desired.CanonicalRepository == ref.CanonicalRepository && slices.Contains(image.Digests, desired.Digest) {
			lookupReference = service.Image
			if !compose.ManagedImage(service) {
				policy = "disabled"
			}
		} else if ref.Digest == "" && editable[name] {
			oldPins[name] = ref.WithDigest(image.Digests[0])
		} else if ref.Digest == "" {
			canApply = false
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
		result, err := s.lookupImage(lookupCtx, node, ipc.ImageLookupPayload{Image: lookupReference, Policy: policy, Digests: image.Digests})
		cancel()
		if err != nil {
			lookupErr = errors.Join(lookupErr, fmt.Errorf("%s: %w", name, err))
			continue
		}
		alerts[name] = ""
		if result.Image == "" {
			continue
		}
		if policy == "disabled" {
			alerts[name] = fmt.Sprintf("%s has a newer image available: %s.", name, result.Image)
		} else if editable[name] {
			candidates[name] = result.Image
		}
	}
	if err := s.syncImageAlerts(ctx, stack, node, "image-upgrade-available", "Workload image upgrade available", "info", alerts, running.Matches && lookupErr == nil); err != nil {
		return err
	}
	if lookupErr != nil || !canApply {
		return lookupErr
	}
	if len(candidates) == 0 {
		return s.db.Execute(ctx, []rqlite.Statement{imageUpgradeSlot(stack.ID, slot)})
	}
	return s.admitStackUpgrade(ctx, stack, instance, spec, oldPins, candidates, slot, deadline)
}

func (s *Server) admitStackUpgrade(ctx context.Context, stack *config.Stack, instance *run.StackInstance, spec *compose.LifecycleSpec, oldPins, candidates map[string]string, slot string, deadline time.Time) error {
	candidate := imageBatchKey(candidates)
	failures, err := failedWorkloadUpgrades(ctx, s.db, stack.ID)
	if err != nil {
		return err
	}
	for _, failure := range failures {
		blocked, err := isFailedImageBatch(candidate, failure)
		if err != nil {
			return err
		}
		if blocked {
			return s.db.Execute(ctx, []rqlite.Statement{imageUpgradeSlot(stack.ID, slot)})
		}
	}
	if !time.Now().Before(deadline) {
		return nil
	}
	rollback := stack.ComposeYAML
	if len(oldPins) > 0 {
		rollback, err = compose.PinImages(rollback, oldPins)
		if err != nil {
			return err
		}
	}
	updated, err := compose.PinImages(rollback, candidates)
	if err != nil {
		return err
	}
	next, err := s.sm.Apply(ctx, instance, run.EventUpdateStack)
	if err != nil {
		return err
	}
	now, id := time.Now().UTC(), uuid.NewString()
	payload := worker.StackUpgradePayload{OperationID: id, StackID: stack.ID, NodeID: stack.PrimaryNodeID, DatasetPath: stack.ZFSDataset,
		OldCompose: stack.ComposeYAML, RollbackCompose: rollback, NewCompose: updated, Candidate: candidate,
		Services: mapKeys(candidates), HealthTimeout: spec.HealthTimeout.String(), Actor: "automatic-upgrade", StartBefore: &deadline,
		Lifecycle: run.StackStateChanged{StackID: stack.ID, From: instance.State, ExpectedUpdatedAt: instance.UpdatedAt, To: next.State, Event: run.EventUpdateStack, Timestamp: now, NodeID: stack.PrimaryNodeID}}
	return s.db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO operations(id,stack_id,action,actor,state,checkpoint,created_at,updated_at) VALUES(?,?,'upgrade',?,'pending','accepted',?,?)`, Params: []any{id, stack.ID, payload.Actor, now, now}},
		worker.TaskInsert("upgrade-"+id, worker.TaskStackUpgrade, stack.ID, payload),
		imageUpgradeSlot(stack.ID, slot),
	})
}

func imageUpgradeSlot(stackID, slot string) rqlite.Statement {
	return rqlite.Statement{SQL: `INSERT INTO scheduler_state(stack_id,tier,last_run_at) VALUES(?,'image-upgrade',?) ON CONFLICT(stack_id,tier) DO UPDATE SET last_run_at=excluded.last_run_at`, Params: []any{stackID, slot}}
}

func imageBatchKey(images map[string]string) string {
	versions := make(map[string]string, len(images))
	for service, image := range images {
		ref, err := compose.ParseImageReference(image)
		version := compose.ParseImageVersion(ref.Tag)
		if err == nil && version.Parts == 3 {
			versions[service] = fmt.Sprintf("%s:%d.%d.%d:%s", ref.CanonicalRepository, version.Numbers[0], version.Numbers[1], version.Numbers[2], version.Variant)
		} else if err == nil {
			ref.Repository = ref.CanonicalRepository
			versions[service] = ref.WithDigest(ref.Digest)
		} else {
			versions[service] = image
		}
	}
	raw, _ := json.Marshal(versions)
	return string(raw)
}

func isFailedImageBatch(candidate, failed string) (bool, error) {
	if candidate == failed {
		return true, nil
	}
	var current, previous map[string]string
	if err := json.Unmarshal([]byte(candidate), &current); err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(failed), &previous); err != nil {
		return false, err
	}
	for service, release := range current {
		if previous[service] != release {
			return false, nil
		}
	}
	return true, nil
}

func failedWorkloadUpgrades(ctx context.Context, db *rqlite.Client, stackID string) ([]string, error) {
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT candidate FROM failed_workload_upgrades WHERE stack_id=? ORDER BY candidate`, Params: []any{stackID}})
	if err != nil {
		return nil, err
	}
	failures := make([]string, 0, len(rows.Values))
	for _, row := range rows.Values {
		failures = append(failures, fmt.Sprint(row[0]))
	}
	return failures, nil
}

func appendFailedWorkloadUpgrades(statements []rqlite.Statement, stackID string, failures []string) []rqlite.Statement {
	for _, failure := range failures {
		statements = append(statements, rqlite.Statement{SQL: `INSERT OR IGNORE INTO failed_workload_upgrades(stack_id,candidate) VALUES(?,?)`, Params: []any{stackID, failure}})
	}
	return statements
}

func (s *Server) syncImageAlerts(ctx context.Context, stack *config.Stack, node, kind, title, severity string, messages map[string]string, complete bool) error {
	rows, err := s.db.Query(ctx, rqlite.Statement{SQL: `SELECT rule_id,message,COALESCE(json_extract(labels,'$.service'),'') FROM alerts WHERE stack_id=? AND kind=? AND state!='resolved'`, Params: []any{stack.ID, kind}})
	if err != nil {
		return err
	}
	active := map[string]string{}
	record := func(id, service, message, action, status string) error {
		now := time.Now().UTC()
		return monitoring.Record(ctx, s.db, s.broker, monitoring.Transition{
			Result: monitoring.Result{CheckID: id, Producer: "workload-upgrade", Kind: kind, Resource: monitoring.Resource{WorkloadID: stack.ID, HostID: node},
				Status: status, EvaluatedAt: now, ObservedAt: now, Labels: map[string]string{"service": service}},
			RuleID: id, Title: title, Message: message, Severity: severity, Action: action, Notify: true,
		})
	}
	for _, row := range rows.Values {
		id, current, service := fmt.Sprint(row[0]), fmt.Sprint(row[1]), fmt.Sprint(row[2])
		message, known := messages[service]
		if current == message || !known && !complete {
			active[service] = current
			continue
		}
		if err := record(id, service, current, "resolve", "healthy"); err != nil {
			return err
		}
	}
	for service, message := range messages {
		if message == "" || active[service] == message {
			continue
		}
		id := fmt.Sprintf("%s:%s:%s", kind, stack.ID, service)
		if kind == "image-upgrade-available" {
			id += fmt.Sprintf(":%x", sha256.Sum256([]byte(message)))
		}
		if err := record(id, service, message, "open", "failing"); err != nil {
			return err
		}
	}
	return nil
}
