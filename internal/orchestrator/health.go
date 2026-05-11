package orchestrator

import (
	"context"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

// handleContainerState reacts to container.state IPC events from hostd.
// A container dying unexpectedly while a stack is RunningPrimary or RunningBackup
// triggers a FailStack event so the FSM can transition to Failed.
func (o *Orchestrator) handleContainerState(ctx context.Context, ev ipc.ContainerStateEvent) {
	if ev.StackID == "" {
		return
	}
	inst, ok := o.raft.FSM().State(ev.StackID)
	if !ok {
		return
	}

	switch ev.Status {
	case "die", "oom":
		if inst.State == run.StateRunningPrimary || inst.State == run.StateRunningBackup {
			o.log.Warn("container died unexpectedly",
				zap.String("stack", ev.StackID),
				zap.String("container", ev.ContainerID),
				zap.String("event", ev.Status))
			o.applyEvent("", ev.StackID, run.EventFailStack)
		}
	}
}

// handleContainerHealth reacts to container.health IPC events from hostd.
// Currently only logs — health-based auto-failover is a Sprint 3 feature.
func (o *Orchestrator) handleContainerHealth(ctx context.Context, ev ipc.ContainerHealthEvent) {
	if ev.StackID == "" {
		return
	}
	if ev.Health == "unhealthy" {
		o.log.Warn("container reported unhealthy",
			zap.String("stack", ev.StackID),
			zap.String("container", ev.ContainerID))
	}
}
