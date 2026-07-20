package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"github.com/google/uuid"
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
		if inst.State != run.StateRunningPrimary && inst.State != run.StateRunningBackup {
			continue
		}

		lifecycle, lifecycleErr := compose.ParseLifecycle(stack.ComposeYAML, stack.Name)
		if lifecycleErr == nil {
			for _, mount := range lifecycle.Mounts {
				for _, tier := range mount.Snapshots {
					if !snapshotDue(now, tier.Interval) {
						continue
					}
					taskID := fmt.Sprintf("snapshot-%s-%s-%s-%s", stack.ID, strings.ReplaceAll(mount.Dataset, "/", "-"), tier.Interval, now.Format("200601021504"))
					payload := worker.SnapshotCreatePayload{Dataset: mount.Dataset, NodeID: stack.PrimaryNodeID, SnapType: tier.Interval, Keep: tier.Retain, Name: "flsnap-" + taskID + "-" + tier.Interval}
					if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
						log.Error("snapshot scheduler: enqueue", zap.String("stack", stack.ID), zap.Error(err))
					} else {
						_ = db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO scheduler_state(stack_id,mount,interval,last_boundary,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(stack_id,mount,interval) DO UPDATE SET last_boundary=excluded.last_boundary,updated_at=excluded.updated_at`, Params: []interface{}{stack.ID, mount.Dataset, tier.Interval, now, time.Now().UTC()}}})
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
		type slot struct {
			name string
			keep int
			due  bool
		}
		slots := []slot{
			{"hourly", sched.Hourly.Keep, now.Minute() == 0},
			{"daily", sched.Daily.Keep, now.Hour() == 0 && now.Minute() == 0},
			{"weekly", sched.Weekly.Keep, now.Weekday() == time.Sunday && now.Hour() == 0 && now.Minute() == 0},
			{"monthly", sched.Monthly.Keep, now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0},
		}

		for _, s := range slots {
			if s.keep == 0 || !s.due {
				continue
			}
			taskID := uuid.New().String()
			payload := worker.SnapshotCreatePayload{
				Dataset:  stack.ZFSDataset,
				NodeID:   stack.PrimaryNodeID,
				SnapType: s.name,
				Keep:     s.keep,
			}
			if err := worker.EnqueueTask(ctx, db, taskID, worker.TaskSnapshotCreate, stack.ID, payload); err != nil {
				log.Error("snapshot scheduler: enqueue",
					zap.String("stack", stack.ID),
					zap.String("type", s.name),
					zap.Error(err),
				)
			} else {
				log.Info("snapshot scheduler: enqueued",
					zap.String("stack", stack.ID),
					zap.String("type", s.name),
				)
			}
		}
	}
}

func snapshotDue(now time.Time, interval string) bool {
	if interval == "" {
		return false
	}
	unit := interval[len(interval)-1:]
	countText := interval[:len(interval)-1]
	if len(interval) > 2 && interval[len(interval)-2:] == "mo" {
		unit, countText = "mo", interval[:len(interval)-2]
	}
	count := 0
	_, _ = fmt.Sscanf(countText, "%d", &count)
	if count < 1 {
		return false
	}
	switch unit {
	case "m":
		return now.Minute()%count == 0
	case "h":
		return now.Minute() == 0 && now.Hour()%count == 0
	case "d":
		return now.Hour() == 0 && now.Minute() == 0 && (now.YearDay()-1)%count == 0
	case "w":
		_, week := now.ISOWeek()
		return now.Weekday() == time.Monday && now.Hour() == 0 && now.Minute() == 0 && (week-1)%count == 0
	case "mo":
		return now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0 && (int(now.Month())-1)%count == 0
	case "y":
		return now.Month() == time.January && now.Day() == 1 && now.Hour() == 0 && now.Minute() == 0 && now.Year()%count == 0
	}
	return false
}
