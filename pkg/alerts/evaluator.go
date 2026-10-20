package alerts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/stats"
)

type Querier interface {
	QueryInstant(context.Context, string, time.Time, time.Duration) ([]stats.InstantSample, error)
}

type Evaluator struct {
	DB       *rqlite.Client
	Metrics  Querier
	Broker   *notify.Broker
	IsLeader func() bool
}

// Tick evaluates each enabled rule independently and preserves incidents on errors.
func (e *Evaluator) Tick(ctx context.Context, rules []Rule, now time.Time) error {
	if !e.IsLeader() {
		return nil
	}
	states, err := loadStates(ctx, e.DB)
	if err != nil {
		return err
	}
	active := map[string]bool{}
	var failures []error
	for _, rule := range rules {
		if !e.IsLeader() || ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if !rule.Enabled {
			continue
		}
		active[rule.ID] = true
		var observations map[string]*Observation
		queryErr := rule.Validate()
		if queryErr == nil {
			queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			observations, queryErr = e.observe(queryCtx, rule, now)
			cancel()
		}
		if queryErr != nil {
			failures = append(failures, fmt.Errorf("rule %s: %w", rule.ID, queryErr))
		}
		previous := map[string]storedState{}
		for _, state := range states[rule.ID] {
			if state.Labels == nil && state.AlertID == "" && len(observations) > 0 && observations[state.ID] == nil {
				if !e.IsLeader() {
					return errors.Join(failures...)
				}
				if _, err := e.DB.Request(ctx, rqlite.Statement{SQL: `DELETE FROM alert_evaluations WHERE id=? AND version=?`, Params: []interface{}{state.ID, state.Version}}); err != nil {
					failures = append(failures, err)
				}
				continue
			}
			previous[state.ID] = state
		}
		if len(previous) == 0 && len(observations) == 0 {
			previous[instanceID(rule, nil)] = storedState{}
		}
		for id := range observations {
			if _, ok := previous[id]; !ok {
				previous[id] = storedState{}
			}
		}
		for id, old := range previous {
			if !e.IsLeader() || ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
			next := advance(rule, old.State, observations[id], now)
			next.Error = ""
			if queryErr != nil {
				next.Error = queryErr.Error()
			}
			if err := save(ctx, e.DB, e.Broker, rule, old, next, now, ""); err != nil {
				failures = append(failures, err)
			}
		}
	}
	for ruleID, instances := range states {
		if active[ruleID] {
			continue
		}
		for _, old := range instances {
			if !e.IsLeader() || ctx.Err() != nil {
				return errors.Join(append(failures, ctx.Err())...)
			}
			if old.Status == "resolved" && old.AlertID == "" {
				continue
			}
			next := old.State
			next.Status, next.LastEvaluated = "resolved", now
			rule := Rule{ID: ruleID, Name: "Alert rule disabled or removed", Severity: old.Severity}
			if err := save(ctx, e.DB, e.Broker, rule, old, next, now, "rule disabled or removed"); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func (e *Evaluator) observe(ctx context.Context, rule Rule, now time.Time) (map[string]*Observation, error) {
	observations := map[string]*Observation{}
	for _, predicate := range []struct {
		Query  string
		Firing bool
	}{{rule.Condition, true}, {rule.RecoveryCondition, false}} {
		if predicate.Query == "" {
			continue
		}
		samples, err := e.Metrics.QueryInstant(ctx, predicate.Query, now, rule.MaxAge)
		if err != nil {
			return nil, err
		}
		for _, sample := range samples {
			labels := map[string]string{}
			for k, v := range sample.Labels {
				if k != "__name__" {
					labels[k] = v
				}
			}
			if rule.StackID != "" && labels["stack_id"] != rule.StackID {
				continue
			}
			if rule.NodeID != "" && labels["node_id"] != rule.NodeID {
				continue
			}
			id := instanceID(rule, labels)
			if previous := observations[id]; previous != nil {
				// Conflicting predicates and duplicate identities are unknown, never healthy.
				previous.Firing, previous.Recovered = true, true
				continue
			}
			observations[id] = &Observation{Labels: labels, Value: sample.Value, At: sample.ObservedAt, Firing: predicate.Firing, Recovered: !predicate.Firing}
		}
	}
	if rule.FreshnessQuery != "" {
		samples, err := e.Metrics.QueryInstant(ctx, rule.FreshnessQuery, now, rule.MaxAge)
		if err != nil {
			return nil, err
		}
		freshness := map[string]time.Time{}
		for _, sample := range samples {
			labels := map[string]string{}
			for k, v := range sample.Labels {
				if k != "__name__" {
					labels[k] = v
				}
			}
			id := instanceID(rule, labels)
			freshness[id] = time.Unix(0, int64(sample.Value*1e9)).UTC()
		}
		for id, observation := range observations {
			observation.At = freshness[id]
		}
	}
	return observations, nil
}
