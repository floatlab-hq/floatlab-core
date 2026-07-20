package failover

import (
	"context"
	"fmt"
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
	db     *rqlite.Client
	store  *config.Store
	raft   *floatraft.Node
	hosts  *hostclient.Pool
	broker *notify.Broker
	log    *zap.Logger
}

func NewSequence(db *rqlite.Client, store *config.Store, raft *floatraft.Node, hosts *hostclient.Pool, broker *notify.Broker, log *zap.Logger) *Sequence {
	return &Sequence{db: db, store: store, raft: raft, hosts: hosts, broker: broker, log: log}
}

// Execute runs the 6-step failover for stackID.
// Primary must be confirmed unreachable by the caller before calling this.
func (s *Sequence) Execute(ctx context.Context, stackID string) error {
	log := s.log.With(zap.String("stack", stackID))

	stack, err := s.store.GetStack(ctx, stackID)
	if err != nil {
		return fmt.Errorf("failover: get stack: %w", err)
	}

	// Step 1: Raft — transition to FailingOver.
	if err := s.raft.Apply(run.StackStateChanged{
		StackID:   stackID,
		From:      run.StateRunningPrimary,
		To:        run.StateFailingOver,
		Event:     run.EventFailoverStart,
		Timestamp: time.Now().UTC(),
	}, 5*time.Second); err != nil {
		return fmt.Errorf("failover: raft FailingOver: %w", err)
	}
	log.Info("failover: step 1/6: raft FailingOver applied")

	// Step 2: Attempt final ZFS sync to secondary (30s deadline).
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	s.attemptFinalSync(syncCtx, stack)
	cancel()
	log.Info("failover: step 2/6: final sync attempted")

	// Step 3: IP takeover — add primary's stack addresses to secondary.
	if err := s.takeoverIPs(ctx, stack); err != nil {
		log.Warn("failover: step 3/6: IP takeover partial", zap.Error(err))
	} else {
		log.Info("failover: step 3/6: IP takeover complete")
	}

	// Step 4: Start containers on secondary.
	if err := s.startOnSecondary(ctx, stack); err != nil {
		// Roll Raft to Failed so orchestrator doesn't get stuck.
		_ = s.raft.Apply(run.StackStateChanged{
			StackID:   stackID,
			From:      run.StateFailingOver,
			To:        run.StateFailed,
			Event:     run.EventFailoverFailed,
			Timestamp: time.Now().UTC(),
		}, 5*time.Second)
		return fmt.Errorf("failover: step 4/6: start on secondary: %w", err)
	}
	log.Info("failover: step 4/6: containers started on secondary")

	// Step 5: Raft — transition to RunningBackup.
	if err := s.raft.Apply(run.StackStateChanged{
		StackID:   stackID,
		From:      run.StateFailingOver,
		To:        run.StateRunningBackup,
		Event:     run.EventFailoverDone,
		Timestamp: time.Now().UTC(),
	}, 5*time.Second); err != nil {
		return fmt.Errorf("failover: raft RunningBackup: %w", err)
	}
	log.Info("failover: step 5/6: raft RunningBackup applied")

	// Step 6: Publish notification.
	_ = notify.Create(ctx, s.db, s.broker, &notify.Notification{
		StackID:  stackID,
		Kind:     "failover",
		Severity: "warning",
		Title:    fmt.Sprintf("Failover complete: %s", stack.Name),
		Body:     fmt.Sprintf("Stack is now running on secondary node %s.", stack.BackupNodeID),
	})
	log.Info("failover: step 6/6: notification published")

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
	s.attemptFinalSync(syncCtx, stack)
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

func (s *Sequence) attemptFinalSync(ctx context.Context, stack *config.Stack) {
	if stack.BackupNodeID == "" {
		return
	}
	dataset := stack.ZFSDataset
	snapshot := fmt.Sprintf("fsrepl-final-%s", time.Now().UTC().Format("20060102-150405"))

	// Create snapshot on primary.
	if _, err := s.hosts.Execute(ctx, stack.PrimaryNodeID, "fs.snapshot.create", ipc.SnapshotCreatePayload{
		Dataset: dataset,
		Name:    snapshot,
	}); err != nil {
		s.log.Warn("failover: final sync: create snapshot failed", zap.Error(err))
		return
	}

	// Send to secondary.
	if _, err := s.hosts.Execute(ctx, stack.PrimaryNodeID, "fs.repl.send", ipc.ReplSendPayload{
		Dataset:  dataset,
		Snapshot: snapshot,
		DestHost: stack.BackupNodeID,
		DestPort: 9696,
	}); err != nil {
		s.log.Warn("failover: final sync: send failed", zap.Error(err))
	}
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
			s.log.Warn("failover: IP takeover: add addr failed", zap.String("addr", addr), zap.Error(err))
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
