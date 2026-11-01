package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/floatlab/floatlab-core/pkg/store"

	"go.uber.org/zap"
)

// RunScheduler starts a minute-tick loop that checks snapshot schedules for
// all running stacks and enqueues worker tasks when a snapshot is due.
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
	stacks, err := orch.store.ListStacks(ctx)
	if err != nil {
		log.Error("snapshot scheduler: list stacks", zap.Error(err))
		return
	}

	for _, stack := range stacks {
		if active, err := orch.ops.Active(ctx); err == nil {
			busy := false
			for _, operation := range active {
				if operation.StackID == stack.ID {
					busy = true
					break
				}
			}
			if busy {
				continue
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
		if inst.State != run.StateRunningPrimary {
			continue
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
				due := false
				for _, tier := range mount.Snapshots {
					due = due || store.SnapshotDue(now, tier.Interval)
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
						due = true
						selected = append(selected, target)
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
				taskID := fmt.Sprintf("snapshot-%s-%s-%s", stack.ID, strings.ReplaceAll(mount.Dataset, "/", "-"), name)
				payload := worker.SnapshotCreatePayload{Dataset: mount.Dataset, NodeID: stack.PrimaryNodeID, StackID: stack.ID, SnapType: "scheduled", Name: name, Policy: localPolicy, OperationID: taskID}
				if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
					log.Error("snapshot scheduler", zap.Error(err))
					continue
				}
				for _, target := range selected {
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
					payload := worker.ReplTriggerPayload{SnapshotTaskID: taskID, Generation: target.Generation, TargetRevision: target.Revision, StackID: stack.ID, Dataset: mount.Dataset, NodeID: stack.PrimaryNodeID, DestNodeID: target.NodeID, DestHost: host, DestPort: 22, Snapshot: name, Policy: target.Schedule}
					if err := worker.EnqueueTask(ctx, db, taskID+"-"+target.NodeID, worker.TaskReplTrigger, stack.ID, payload); err != nil {
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
		if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
			log.Error("snapshot scheduler", zap.Error(err))
		}
	}
}

func snapshotDue(now time.Time, interval string) bool {
	return store.SnapshotDue(now, interval)
}
