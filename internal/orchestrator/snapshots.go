package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/floatlab/floatlab-core/pkg/store"

	"go.uber.org/zap"
)

// RunScheduler starts a minute-tick loop that checks snapshot schedules for
// provisioned stacks and enqueues worker tasks when a snapshot is due.
// It aligns to the wall-clock minute boundary before starting.
func RunScheduler(ctx context.Context, orch *Orchestrator, db *rqlite.Client, log *zap.Logger) {
	now := time.Now().UTC()
	wait := time.Until(now.Truncate(time.Minute).Add(time.Minute))
	select {
	case <-ctx.Done():
		return
	case <-time.After(wait):
	}

	scheduleTick(ctx, orch, db, time.Now().UTC().Truncate(time.Minute), log)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			scheduleTick(ctx, orch, db, t.UTC(), log)
		}
	}
}

func scheduleTick(ctx context.Context, orch *Orchestrator, db *rqlite.Client, now time.Time, log *zap.Logger) {
	stacks, err := orch.store.ListStackCatalog(ctx)
	if err != nil {
		log.Error("snapshot scheduler: list stacks", zap.Error(err))
		return
	}

	for _, stack := range stacks {
		busy := false
		if active, err := orch.ops.Active(ctx); err == nil {
			for _, operation := range active {
				if operation.StackID == stack.ID && operation.Action != "stop" {
					busy = true
					break
				}
			}
		}
		inst, ok := orch.raft.FSM().State(stack.ID)
		if !ok {
			continue
		}
		if inst.State == run.StateRunningBackup {
			// shortcut: policies retain primary ownership, migrate recovery points before enabling failover-source backups.
			log.Warn("backup scheduling suspended: workload active on failover node", zap.String("stack", stack.ID))
			continue
		}
		if !scheduledProtectionState(inst, stack.PrimaryNodeID) {
			continue
		}
		if stack.ComposeFile {
			stack, err = orch.store.GetStack(ctx, stack.ID)
			if err != nil {
				log.Warn("snapshot scheduler: read Compose", zap.Error(err))
				continue
			}
		}

		lifecycle, lifecycleErr := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
		if lifecycleErr == nil {
			targets, err := orch.store.BackupTargets(ctx, stack.ID)
			if err != nil {
				log.Error("backup targets", zap.Error(err))
				continue
			}
			for _, mount := range lifecycle.Mounts {
				localPolicy := config.MountPolicy(mount)
				localDue := false
				for _, tier := range mount.Snapshots {
					localDue = localDue || store.SnapshotDue(now, tier.Interval)
				}
				var selected []config.BackupTarget
				for _, target := range targets {
					if !target.Enabled || target.Dataset != mount.Dataset {
						continue
					}
					tiers, err := store.ParseSnapshotPolicy(target.Schedule)
					if err != nil {
						continue
					}
					targetDue := false
					for _, tier := range tiers {
						targetDue = targetDue || store.SnapshotDue(now, tier.Interval)
					}
					if targetDue {
						selected = append(selected, target)
					}
				}
				if !localDue && len(selected) == 0 {
					continue
				}
				name, err := store.AutomatedSnapshotName(now)
				if err != nil {
					log.Error("snapshot timestamp", zap.Error(err))
					continue
				}
				taskID := fmt.Sprintf("snapshot-%s-%s-%s", stack.ID, strings.ReplaceAll(mount.Dataset, "/", "-"), name)
				payload := worker.SnapshotCreatePayload{Dataset: mount.Dataset, NodeID: stack.PrimaryNodeID, StackID: stack.ID, SnapType: "scheduled", Name: name, Policy: localPolicy, OperationID: taskID}
				if localDue {
					if err := saveProtectionOccurrence(ctx, db, taskID, "snapshot", stack, mount.Dataset, "", 0, now); err != nil {
						log.Error("snapshot occurrence", zap.Error(err))
					}
				}
				snapshotQueued := !busy
				if snapshotQueued {
					if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
						log.Error("snapshot scheduler", zap.Error(err))
						snapshotQueued = false
					}
				}
				for _, target := range selected {
					id := taskID + "-" + target.NodeID
					if err := saveProtectionOccurrence(ctx, db, id, "replication", stack, mount.Dataset, target.NodeID, target.Generation, now); err != nil {
						log.Error("replication occurrence", zap.Error(err))
					}
					if !snapshotQueued {
						continue
					}
					dest, err := orch.store.GetNode(ctx, target.NodeID)
					if err != nil {
						continue
					}
					host := ""
					for _, address := range dest.Addresses {
						if address.Type == "LAN-6" {
							host = address.Address
							break
						}
					}
					if host == "" {
						continue
					}
					payload := worker.ReplTriggerPayload{SnapshotTaskID: taskID, Scheduled: true, Generation: target.Generation, TargetRevision: target.Revision, StackID: stack.ID, Dataset: mount.Dataset, NodeID: stack.PrimaryNodeID, DestNodeID: target.NodeID, DestHost: host, DestPort: 22, Snapshot: name, Policy: target.Schedule}
					if err := worker.EnqueueTask(ctx, db, id, worker.TaskReplTrigger, stack.ID, payload); err != nil {
						log.Error("backup scheduler", zap.Error(err))
					}
				}
			}
			continue
		}

		parsed, err := compose.Parse(ctx, stack.ComposeYAML, stack.Name)
		if err != nil {
			log.Warn("snapshot scheduler: parse compose", zap.String("stack", stack.ID), zap.Error(err))
			continue
		}

		sched := parsed.Extension.Snapshots.Schedule
		slots := []struct {
			interval string
			keep     int
		}{{"1h", sched.Hourly.Keep}, {"1d", sched.Daily.Keep}, {"1w", sched.Weekly.Keep}, {"1mo", sched.Monthly.Keep}}
		due := false
		policy := []string{}
		for _, slot := range slots {
			if slot.keep > 0 {
				policy = append(policy, fmt.Sprintf("%s/%d", slot.interval, slot.keep))
				due = due || store.SnapshotDue(now, slot.interval)
			}
		}
		if !due {
			continue
		}
		name, err := store.AutomatedSnapshotName(now)
		if err != nil {
			log.Error("snapshot timestamp", zap.Error(err))
			continue
		}
		taskID := fmt.Sprintf("snapshot-%s-%s", stack.ID, name)
		payload := worker.SnapshotCreatePayload{StackID: stack.ID, Dataset: stack.ZFSDataset, NodeID: stack.PrimaryNodeID, SnapType: "scheduled", Name: name, Policy: strings.Join(policy, " "), OperationID: taskID}
		if err := saveProtectionOccurrence(ctx, db, taskID, "snapshot", stack, stack.ZFSDataset, "", 0, now); err != nil {
			log.Error("snapshot occurrence", zap.Error(err))
		}
		if busy {
			continue
		}
		if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
			log.Error("snapshot scheduler", zap.Error(err))
		}
	}
}

func scheduledProtectionState(stack *run.StackInstance, primary string) bool {
	if (stack.NodeID != "" && stack.NodeID != primary) || (stack.State == run.StateStopping && stack.PreviousState == run.StateRunningBackup) {
		return false
	}
	return stack.State == run.StateRunningPrimary || stack.State == run.StateIdle || stack.State == run.StateStopping
}

func saveProtectionOccurrence(ctx context.Context, db *rqlite.Client, id, kind string, stack *config.Stack, dataset, destination string, generation int64, now time.Time) error {
	return monitoring.SaveExecution(ctx, db, monitoring.Execution{
		ID: id, Kind: kind, State: "scheduled", Generation: generation, ScheduledAt: now.UTC().Truncate(time.Minute),
		Resource: monitoring.Resource{HostID: stack.PrimaryNodeID, WorkloadID: stack.ID, Dataset: dataset, SourceID: stack.PrimaryNodeID, DestinationID: destination},
	})
}

func snapshotDue(now time.Time, interval string) bool {
	return store.SnapshotDue(now, interval)
}
