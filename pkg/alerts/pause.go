package alerts

import "time"

func pause(rule Rule, old State, observation *Observation, now time.Time) (State, bool) {
	stackID := old.Labels["stack_id"]
	if observation != nil {
		stackID = observation.Labels["stack_id"]
	}
	if !rule.Paused && !rule.PausedStacks[stackID] {
		return State{}, false
	}
	next := old
	if observation != nil {
		next.Labels = observation.Labels
	}
	next.Status, next.Error = "paused", ""
	next.LastEvaluated, next.ResumeAfter = now, now
	next.resetTimers()
	return next, true
}
