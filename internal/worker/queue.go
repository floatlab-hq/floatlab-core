package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/store"
	"go.uber.org/zap"
)

const (
	pollInterval  = 5 * time.Second
	maxAttempts   = 5
	backoffFactor = 60 // attempt N waits N*backoffFactor seconds
)

// Handler processes a single task payload, returning an error to trigger retry.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Worker polls the rqlite tasks table and dispatches tasks via IPC to hostd.
type Worker struct {
	db       *rqlite.Client
	store    *config.Store
	pool     *hostclient.Pool
	hostname string
	handlers map[string]Handler
	log      *zap.Logger
}

func New(db *rqlite.Client, cfgStore *config.Store, pool *hostclient.Pool, log *zap.Logger) *Worker {
	h, _ := os.Hostname()
	w := &Worker{
		db:       db,
		store:    cfgStore,
		pool:     pool,
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

func (w *Worker) poll(ctx context.Context) error {
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

	if execErr := handler(ctx, json.RawMessage(payloadStr)); execErr != nil {
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
		return fmt.Errorf("snapshot.create: parse: %w", err)
	}
	var name string
	if p.SnapType == "user" {
		name = store.UserSnapshotName(p.Label)
	} else {
		name = store.ScheduledSnapshotName(p.SnapType)
	}
	if _, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{
		Dataset: p.Dataset,
		Name:    name,
	}); err != nil {
		return fmt.Errorf("snapshot.create %s: %w", p.Dataset, err)
	}
	if p.Keep > 0 {
		if err := w.applyRetentionViaIPC(ctx, p.NodeID, p.Dataset, p.SnapType, p.Keep); err != nil {
			w.log.Warn("worker: retention cleanup failed", zap.String("dataset", p.Dataset), zap.Error(err))
		}
	}
	return nil
}

func (w *Worker) handleSnapshotDelete(ctx context.Context, raw json.RawMessage) error {
	var p SnapshotDeletePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("snapshot.delete: parse: %w", err)
	}
	_, err := w.pool.Execute(ctx, p.NodeID, "fs.snapshot.destroy", ipc.SnapshotDestroyPayload{
		Dataset: p.Dataset,
		Name:    p.Snapshot,
	})
	return err
}

func (w *Worker) handleReplTrigger(ctx context.Context, raw json.RawMessage) error {
	var p ReplTriggerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("repl.trigger: parse: %w", err)
	}
	jobID := fmt.Sprintf("repl-%s-%d", p.StackID, time.Now().UnixMilli())
	_, err := w.pool.Execute(ctx, p.NodeID, "fs.repl.send", ipc.ReplSendPayload{
		Dataset:  p.Dataset,
		DestHost: p.DestHost,
		DestPort: p.DestPort,
		JobID:    jobID,
	})
	return err
}

// applyRetentionViaIPC lists snapshots of the given type and destroys those
// beyond the keep limit, using IPC calls to the target node.
func (w *Worker) applyRetentionViaIPC(ctx context.Context, nodeID, dataset, snapType string, keep int) error {
	raw, err := w.pool.Execute(ctx, nodeID, "fs.snapshot.list", ipc.SnapshotListPayload{Dataset: dataset})
	if err != nil {
		return fmt.Errorf("retention list: %w", err)
	}
	var result ipc.SnapshotListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("retention parse: %w", err)
	}

	prefix := store.PrefixScheduled + "-"
	suffix := "-" + snapType
	var matching []ipc.SnapshotInfoResult
	for _, s := range result.Snapshots {
		if strings.HasPrefix(s.Name, prefix) && strings.HasSuffix(s.Name, suffix) {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool { return matching[i].Name > matching[j].Name })

	for i := keep; i < len(matching); i++ {
		if _, err := w.pool.Execute(ctx, nodeID, "fs.snapshot.destroy", ipc.SnapshotDestroyPayload{
			Dataset: matching[i].Dataset,
			Name:    matching[i].Name,
		}); err != nil {
			return fmt.Errorf("retention destroy %s@%s: %w", matching[i].Dataset, matching[i].Name, err)
		}
	}
	return nil
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
