package orchestrator

import (
	"context"
	"encoding/json"

	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/operation"
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
			if operations, err := o.ops.Active(ctx); err == nil {
				for _, active := range operations {
					if active.StackID == ev.StackID && (active.Action == "stop" || active.Action == "restart" || active.Action == "delete") {
						return
					}
				}
			}
			// Docker events may arrive after a restart operation has completed.
			// Confirm this container still exists and is stopped before failing the stack.
			stack, err := o.store.GetStack(ctx, ev.StackID)
			if err != nil {
				return
			}
			nodeID := stack.PrimaryNodeID
			if inst.State == run.StateRunningBackup {
				nodeID = stack.BackupNodeID
			}
			raw, err := o.pool.Execute(ctx, nodeID, "docker.list", ipc.DockerListPayload{StackID: ev.StackID})
			var current ipc.DockerListResult
			if err == nil {
				err = json.Unmarshal(raw, &current)
			}
			if err != nil {
				o.log.Warn("could not verify container stop event", zap.String("stack", ev.StackID), zap.Error(err))
				return
			}
			stopped := false
			for _, container := range current.Containers {
				if container.ID == ev.ContainerID {
					stopped = container.State == "exited" || container.State == "dead" || container.State == "stopped"
					break
				}
			}
			if !stopped {
				return
			}
			o.log.Warn("container died unexpectedly",
				zap.String("stack", ev.StackID),
				zap.String("container", ev.ContainerID),
				zap.String("event", ev.Status))
			o.applyEvent("", ev.StackID, run.EventFailStack)
			details, _ := json.Marshal(map[string]string{"container_id": ev.ContainerID, "service": ev.Service, "image": ev.Image, "exit_status": ev.ExitStatus})
			containers, _ := json.Marshal([]string{ev.ContainerID})
			_ = operation.RecordEvent(ctx, o.db, operation.Event{StackID: ev.StackID, Type: "Crashed", Outcome: "failed", Details: details, Containers: containers})
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
