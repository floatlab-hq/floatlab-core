package failover

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	floatraft "github.com/floatlab/floatlab-core/pkg/raft"
	"github.com/floatlab/floatlab-core/pkg/run"
)

const pingInterval = 10 * time.Second

// Detector polls the primary node for each RunningPrimary stack.
// When the primary is unreachable for longer than the stack's
// auto_trigger_after duration (mode=auto), it calls seq.Execute.
type Detector struct {
	store    *config.Store
	fsm      *floatraft.FSM
	hosts    *hostclient.Pool
	seq      *Sequence
	log      *zap.Logger
	// downSince tracks when each stack first became unreachable. key: stack ID.
	downSince map[string]time.Time
}

func NewDetector(store *config.Store, fsm *floatraft.FSM, hosts *hostclient.Pool, seq *Sequence, log *zap.Logger) *Detector {
	return &Detector{
		store:     store,
		fsm:       fsm,
		hosts:     hosts,
		seq:       seq,
		log:       log,
		downSince: make(map[string]time.Time),
	}
}

// Run starts the detector loop. It returns only when ctx is cancelled.
func (d *Detector) Run(ctx context.Context) {
	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.tick(ctx)
		}
	}
}

func (d *Detector) tick(ctx context.Context) {
	stacks, err := d.store.ListStacks(ctx)
	if err != nil {
		d.log.Warn("detector: list stacks", zap.Error(err))
		return
	}

	activeIDs := make(map[string]bool)
	for _, st := range stacks {
		inst, ok := d.fsm.State(st.ID)
		if !ok || inst.State != run.StateRunningPrimary {
			delete(d.downSince, st.ID)
			continue
		}
		activeIDs[st.ID] = true

		alive := d.ping(ctx, st.PrimaryNodeID)
		if alive {
			delete(d.downSince, st.ID)
			continue
		}

		if _, tracked := d.downSince[st.ID]; !tracked {
			d.downSince[st.ID] = time.Now()
			d.log.Warn("detector: primary unreachable", zap.String("stack", st.ID), zap.String("node", st.PrimaryNodeID))
			continue
		}

		if st.FailoverMode != "auto" || st.AutoTriggerAfter == "" {
			continue
		}

		threshold, err := time.ParseDuration(st.AutoTriggerAfter)
		if err != nil {
			continue
		}

		if time.Since(d.downSince[st.ID]) >= threshold {
			d.log.Info("detector: auto-triggering failover",
				zap.String("stack", st.ID),
				zap.Duration("down_for", time.Since(d.downSince[st.ID])),
			)
			delete(d.downSince, st.ID)
			go func(stackID string) {
				if err := d.seq.Execute(ctx, stackID); err != nil {
					d.log.Error("detector: failover execute", zap.String("stack", stackID), zap.Error(err))
				}
			}(st.ID)
		}
	}

	// Clean up stacks that no longer exist.
	for id := range d.downSince {
		if !activeIDs[id] {
			delete(d.downSince, id)
		}
	}
}

func (d *Detector) ping(ctx context.Context, nodeID string) bool {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := d.hosts.Execute(pingCtx, nodeID, "sys.info", nil)
	return err == nil
}
