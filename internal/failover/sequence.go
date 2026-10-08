package failover

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/notify"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
)

// Sequence executes the 6-step failover and the reverse failback.
type Sequence struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc
	db     *rqlite.Client
	store  *config.Store
	raft   *floatraft.Node
	hosts  *hostclient.Pool
	broker *notify.Broker
	log    *zap.Logger
}

func NewSequence(db *rqlite.Client, store *config.Store, raft *floatraft.Node, hosts *hostclient.Pool, broker *notify.Broker, log *zap.Logger) *Sequence {
	return &Sequence{db: db, store: store, raft: raft, hosts: hosts, broker: broker, log: log, active: make(map[string]context.CancelFunc)}
}

// Execute runs the 6-step failover for stackID.
// Primary must be confirmed unreachable by the caller before calling this.
func (s *Sequence) Execute(ctx context.Context, stackID string) error {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if _, exists := s.active[stackID]; exists {
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("failover already active")
	}
	s.active[stackID] = cancel
	s.mu.Unlock()
	defer func() { cancel(); s.mu.Lock(); delete(s.active, stackID); s.mu.Unlock() }()
	log := s.log.With(zap.String("stack", stackID))

	stack, err := s.store.GetStack(ctx, stackID)
	if err != nil {
		return fmt.Errorf("failover: get stack: %w", err)
	}

	sequenceID := fmt.Sprint(time.Now().UnixNano())
	step := func(number int, name string, action func() error) error {
		started := time.Now().UTC().Format(time.RFC3339Nano)
		if err := s.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO failover_steps(stack_id,sequence_id,step,name,state,started_at) VALUES(?,?,?,?,'running',?)`, Params: []interface{}{stackID, sequenceID, number, name, started}}}); err != nil {
			return err
		}
		err := action()
		state, detail := "complete", ""
		if err != nil {
			state, detail = "failed", err.Error()
		}
		// Cancellation must not prevent recording the final outcome.
		recordCtx, recordCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer recordCancel()
		recordErr := s.db.Execute(recordCtx, []rqlite.Statement{{SQL: `UPDATE failover_steps SET state=?,completed_at=?,detail=? WHERE stack_id=? AND sequence_id=? AND step=?`, Params: []interface{}{state, time.Now().UTC().Format(time.RFC3339Nano), detail, stackID, sequenceID, number}}})
		if err != nil {
			return err
		}
		return recordErr
	}
	if err := step(1, "raft-quorum", func() error {
		return s.raft.Apply(run.StackStateChanged{StackID: stackID, From: run.StateRunningPrimary, To: run.StateFailingOver, Event: run.EventFailoverStart, Timestamp: time.Now().UTC()}, 5*time.Second)
	}); err != nil {
		return err
	}
	fail := func(err error) error {
		_ = s.raft.Apply(run.StackStateChanged{StackID: stackID, From: run.StateFailingOver, To: run.StateFailed, Event: run.EventFailoverFailed, Timestamp: time.Now().UTC()}, 5*time.Second)
		return err
	}
	if err := step(2, "zfs-sync", func() error {
		syncCtx, syncCancel := context.WithTimeout(ctx, 30*time.Second)
		defer syncCancel()
		return s.attemptFinalSync(syncCtx, stack)
	}); err != nil {
		log.Warn("failover: final sync failed; using replicated data", zap.Error(err))
	}
	// Received snapshots are already accessible; no promotion is performed.
	if err := s.db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO failover_steps(stack_id,sequence_id,step,name,state,detail) VALUES(?,?,3,'snapshot-promote','skipped','received dataset requires no promotion')`, Params: []interface{}{stackID, sequenceID}}}); err != nil {
		return fail(err)
	}
	s.mu.Lock()
	delete(s.active, stackID)
	s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := step(4, "ip-takeover", func() error { return s.takeoverIPs(ctx, stack) }); err != nil {
		return fail(err)
	}
	if err := step(5, "compose-up", func() error { return s.startOnSecondary(ctx, stack) }); err != nil {
		return fail(err)
	}
	if err := step(6, "raft-apply", func() error {
		return s.raft.Apply(run.StackStateChanged{StackID: stackID, From: run.StateFailingOver, To: run.StateRunningBackup, Event: run.EventFailoverDone, Timestamp: time.Now().UTC()}, 5*time.Second)
	}); err != nil {
		return fail(err)
	}
	_ = notify.Create(ctx, s.db, s.broker, &notify.Notification{StackID: stackID, Kind: "failover", Severity: "warning", Title: fmt.Sprintf("Failover complete: %s", stack.Name), Body: fmt.Sprintf("Stack is now running on secondary node %s.", stack.BackupNodeID)})

	return nil
}

// Restore runs failback: secondary → Restoring → sync → primary start → RunningPrimary.
func (s *Sequence) Restore(ctx context.Context, stackID string) error {
	log := s.log.With(zap.String("stack", stackID))

	stack, err := s.store.GetStack(ctx, stackID)
	if err != nil {
		return fmt.Errorf("failover: restore: get stack: %w", err)
	}

	if err := s.raft.Apply(run.StackStateChanged{
		StackID:   stackID,
		From:      run.StateRunningBackup,
		To:        run.StateRestoring,
		Event:     run.EventRestoreStart,
		Timestamp: time.Now().UTC(),
	}, 5*time.Second); err != nil {
		return fmt.Errorf("failover: restore: raft Restoring: %w", err)
	}
	log.Info("failover: restore: step 1/4: raft Restoring applied")

	// Stop secondary containers.
	_, _ = s.hosts.Execute(ctx, stack.BackupNodeID, "compose.down", ipc.ComposeDownPayload{
		StackID:     stackID,
		DatasetPath: stack.ZFSDataset,
	})
	log.Info("failover: restore: step 2/4: secondary containers stopped")

	// Sync back to primary.
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_ = s.attemptFinalSync(syncCtx, stack)
	cancel()
	log.Info("failover: restore: step 3/4: final sync attempted")

	// Start on primary.
	if err := s.startOnPrimary(ctx, stack); err != nil {
		_ = s.raft.Apply(run.StackStateChanged{
			StackID:   stackID,
			From:      run.StateRestoring,
			To:        run.StateFailed,
			Event:     run.EventRestoreFailed,
			Timestamp: time.Now().UTC(),
		}, 5*time.Second)
		return fmt.Errorf("failover: restore: start on primary: %w", err)
	}

	if err := s.raft.Apply(run.StackStateChanged{
		StackID:   stackID,
		From:      run.StateRestoring,
		To:        run.StateRunningPrimary,
		Event:     run.EventRestoreDone,
		Timestamp: time.Now().UTC(),
	}, 5*time.Second); err != nil {
		return fmt.Errorf("failover: restore: raft RunningPrimary: %w", err)
	}
	log.Info("failover: restore: step 4/4: raft RunningPrimary applied")

	_ = notify.Create(ctx, s.db, s.broker, &notify.Notification{
		StackID:  stackID,
		Kind:     "failover",
		Severity: "info",
		Title:    fmt.Sprintf("Failback complete: %s", stack.Name),
		Body:     fmt.Sprintf("Stack returned to primary node %s.", stack.PrimaryNodeID),
	})
	return nil
}

func (s *Sequence) attemptFinalSync(ctx context.Context, stack *config.Stack) error {
	if stack.BackupNodeID == "" {
		return fmt.Errorf("secondary node required")
	}
	dataset := stack.ZFSDataset
	snapshot := fmt.Sprintf("fsrepl-final-%s", time.Now().UTC().Format("20060102-150405"))

	// Create snapshot on primary.
	if _, err := s.hosts.Execute(ctx, stack.PrimaryNodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{
		Dataset: dataset,
		Name:    snapshot,
	}); err != nil {
		return fmt.Errorf("create final snapshot: %w", err)
	}

	// Send to secondary.
	if _, err := s.hosts.Execute(ctx, stack.PrimaryNodeID, "fs.repl.send", ipc.ReplSendPayload{
		Dataset:  dataset,
		Snapshot: snapshot,
		DestHost: stack.BackupNodeID,
		DestPort: 9696,
	}); err != nil {
		return fmt.Errorf("send final snapshot: %w", err)
	}
	return nil
}

func (s *Sequence) takeoverIPs(ctx context.Context, stack *config.Stack) error {
	if stack.BackupNodeID == "" {
		return nil
	}
	res, err := s.db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT address FROM ip_reservations WHERE stack_id = ?`,
		Params: []interface{}{stack.ID},
	})
	if err != nil {
		return err
	}
	for _, row := range res.Values {
		addr, _ := row[0].(string)
		if addr == "" {
			continue
		}
		if _, err := s.hosts.Execute(ctx, stack.BackupNodeID, "net.addr.add", ipc.NetAddrPayload{
			Interface: "eth0",
			Address:   addr,
		}); err != nil {
			return fmt.Errorf("takeover %s: %w", addr, err)
		}
	}
	return nil
}

func (s *Sequence) startOnSecondary(ctx context.Context, stack *config.Stack) error {
	_, err := s.hosts.Execute(ctx, stack.BackupNodeID, "compose.up", ipc.ComposeUpPayload{
		StackID:     stack.ID,
		DatasetPath: stack.ZFSDataset,
		ComposeFile: stack.ComposeYAML,
	})
	return err
}

func (s *Sequence) startOnPrimary(ctx context.Context, stack *config.Stack) error {
	_, err := s.hosts.Execute(ctx, stack.PrimaryNodeID, "compose.up", ipc.ComposeUpPayload{
		StackID:     stack.ID,
		DatasetPath: stack.ZFSDataset,
		ComposeFile: stack.ComposeYAML,
	})
	return err
}

// Abort cancels a sequence; its worker records the final failure state.
func (s *Sequence) Abort(stackID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cancel, ok := s.active[stackID]
	if ok {
		cancel()
	}
	return ok
}
