package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
	"go.uber.org/zap"
)

const (
	pollInterval  = 5 * time.Second
	maxAttempts   = 5
	backoffFactor = 60 // seconds; attempt N waits N*backoffFactor seconds
)

// Handler processes a task by type. It receives the raw JSON payload and
// returns an error to mark the task failed (retryable), or nil on success.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Worker polls the rqlite tasks table and dispatches tasks to registered handlers.
// Tasks are claimed with a hostname lock to prevent double execution in a multi-node
// control-plane deployment. Exponential backoff delays retries for failing tasks.
type Worker struct {
	db       *rqlite.Client
	zfs      store.ZFSStore
	hostname string
	handlers map[string]Handler
	log      *zap.Logger
}

func New(db *rqlite.Client, zfs store.ZFSStore, log *zap.Logger) *Worker {
	h, _ := os.Hostname()
	w := &Worker{
		db:       db,
		zfs:      zfs,
		hostname: h,
		handlers: make(map[string]Handler),
		log:      log,
	}
	w.registerHandlers()
	return w
}

func (w *Worker) registerHandlers() {
	w.handlers[TaskSnapshotCreate] = w.handleSnapshotCreate
	w.handlers[TaskSnapshotDelete] = w.handleSnapshotDelete
	w.handlers[TaskReplTrigger] = w.handleReplTrigger
}

// Run polls for pending tasks on the configured interval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := w.poll(ctx); err != nil {
				w.log.Error("worker: poll error", zap.Error(err))
			}
		}
	}
}

// poll claims and executes up to one pending task per call.
func (w *Worker) poll(ctx context.Context) error {
	// Claim a pending task that is either on its first attempt or past its backoff window.
	// Backoff window: attempts * backoffFactor seconds since last update.
	claimSQL := `
		UPDATE tasks
		SET state = 'running', locked_by = ?, updated_at = datetime('now')
		WHERE id = (
			SELECT id FROM tasks
			WHERE state = 'pending'
			  AND attempts < ?
			  AND (
			      attempts = 0
			      OR updated_at <= datetime('now', '-' || (attempts * ?) || ' seconds')
			  )
			ORDER BY created_at ASC
			LIMIT 1
		)
		RETURNING id, type, payload, attempts`

	result, err := w.db.Query(ctx, rqlite.Statement{
		SQL:    claimSQL,
		Params: []interface{}{w.hostname, maxAttempts, backoffFactor},
	})
	if err != nil {
		return fmt.Errorf("worker: claim query: %w", err)
	}
	if len(result.Values) == 0 {
		return nil
	}

	row := result.Values[0]
	taskID, _ := row[0].(string)
	taskType, _ := row[1].(string)
	payloadStr, _ := row[2].(string)
	attempts, _ := row[3].(float64)

	w.log.Info("worker: executing task",
		zap.String("id", taskID),
		zap.String("type", taskType),
		zap.Int("attempt", int(attempts)+1),
	)

	handler, ok := w.handlers[taskType]
	if !ok {
		w.failTask(ctx, taskID, fmt.Sprintf("no handler for task type %q", taskType))
		return nil
	}

	execErr := handler(ctx, json.RawMessage(payloadStr))
	if execErr != nil {
		w.log.Error("worker: task failed",
			zap.String("id", taskID),
			zap.String("type", taskType),
			zap.Error(execErr),
		)
		w.failTask(ctx, taskID, execErr.Error())
		return nil
	}

	return w.db.Execute(ctx, []rqlite.Statement{{
		SQL:    `UPDATE tasks SET state = 'done', updated_at = datetime('now'), locked_by = NULL WHERE id = ?`,
		Params: []interface{}{taskID},
	}})
}

func (w *Worker) failTask(ctx context.Context, id, errMsg string) {
	_ = w.db.Execute(ctx, []rqlite.Statement{{
		SQL: `UPDATE tasks
		      SET state = 'pending', attempts = attempts + 1, error = ?, updated_at = datetime('now'), locked_by = NULL
		      WHERE id = ?`,
		Params: []interface{}{errMsg, id},
	}})
}

func (w *Worker) handleSnapshotCreate(ctx context.Context, raw json.RawMessage) error {
	var p SnapshotCreatePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("snapshot.create: parse payload: %w", err)
	}
	var name string
	if p.SnapType == "user" {
		name = store.UserSnapshotName(p.Label)
	} else {
		name = store.ScheduledSnapshotName(p.SnapType)
	}
	if err := w.zfs.SnapshotCreate(ctx, p.Dataset, name); err != nil {
		return fmt.Errorf("snapshot.create %s: %w", p.Dataset, err)
	}
	if p.Keep > 0 {
		if err := store.ApplyRetention(ctx, w.zfs, p.Dataset, p.SnapType, p.Keep); err != nil {
			w.log.Warn("worker: retention cleanup failed", zap.String("dataset", p.Dataset), zap.Error(err))
		}
	}
	return nil
}

func (w *Worker) handleSnapshotDelete(ctx context.Context, raw json.RawMessage) error {
	var p SnapshotDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("snapshot.delete: parse payload: %w", err)
	}
	return w.zfs.SnapshotDestroy(ctx, p.Dataset, p.Snapshot)
}

func (w *Worker) handleReplTrigger(ctx context.Context, raw json.RawMessage) error {
	var p ReplTriggerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("repl.trigger: parse payload: %w", err)
	}
	newSnap, baseSnap, err := store.PlanReplication(ctx, w.zfs, p.Dataset)
	if err != nil {
		return err
	}
	jobID := fmt.Sprintf("repl-%s-%d", p.StackID, time.Now().UnixMilli())
	_, err = store.SendReplication(ctx, jobID, p.Dataset, newSnap, baseSnap, p.DestHost, p.DestPort)
	return err
}

// EnqueueTask inserts a new pending task into the rqlite tasks table.
func EnqueueTask(ctx context.Context, db *rqlite.Client, id, taskType, stackID string, payload interface{}) error {
	p := marshalPayload(payload)
	return db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO tasks (id, type, stack_id, payload, state, attempts, created_at, updated_at)
		      VALUES (?, ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		Params: []interface{}{id, taskType, stackID, p},
	}})
}
