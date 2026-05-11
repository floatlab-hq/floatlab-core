package orchestrator

import (
	"context"
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
		inst, ok := orch.raft.FSM().State(stack.ID)
		if !ok {
			continue
		}
		if inst.State != run.StateRunningPrimary && inst.State != run.StateRunningBackup {
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
