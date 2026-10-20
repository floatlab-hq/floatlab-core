// Package alerts evaluates persistent, per-resource alert conditions.
package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

const Interval = 30 * time.Second
const DefaultMaxAge = 2 * time.Minute

// Rule uses filtered MetricsQL predicates: every returned series is a match,
// including a series with value zero. Recovery requires a separate predicate.
type Rule struct {
	ID, Name, Condition, RecoveryCondition, FreshnessQuery, Severity, StackID, NodeID, Message string
	For, RecoveryFor, MaxAge                                                                   time.Duration
	Enabled                                                                                    bool
	Compose                                                                                    bool
}

func (r Rule) Validate() error {
	if r.ID == "" || r.Name == "" || r.Condition == "" {
		return fmt.Errorf("rule id, name and condition are required")
	}
	if r.RecoveryCondition == "" || r.FreshnessQuery == "" {
		return fmt.Errorf("recovery_condition and freshness_query are required for persistent rules")
	}
	if r.Severity != "info" && r.Severity != "warning" && r.Severity != "critical" {
		return fmt.Errorf("invalid rule severity")
	}
	if r.For < 0 || r.RecoveryFor < 0 || r.MaxAge <= 0 {
		return fmt.Errorf("invalid rule duration")
	}
	return nil
}

type Observation struct {
	Labels            map[string]string
	Value             float64
	At                time.Time
	Firing, Recovered bool
}

type State struct {
	RuleHash                                                 string
	Labels                                                   map[string]string
	Status                                                   string
	Error                                                    string
	PendingSince, RecoverySince, LastObserved, LastEvaluated time.Time
	AlertID, Severity                                        string
	Value                                                    *float64
}

func fingerprint(ruleID string, labels map[string]string) string {
	b, _ := json.Marshal(labels)
	sum := sha256.Sum256(append([]byte(ruleID+"\x00"), b...))
	return hex.EncodeToString(sum[:])
}

func instanceID(rule Rule, labels map[string]string) string {
	if rule.Compose {
		identity := map[string]string{}
		for key, value := range labels {
			if key != "node_id" {
				identity[key] = value
			}
		}
		return fingerprint(rule.ID, identity)
	}
	return fingerprint(rule.ID, labels)
}

func ruleHash(r Rule) string {
	// Severity/message changes update the same incident; condition changes reset timers.
	b, _ := json.Marshal([]interface{}{r.Condition, r.RecoveryCondition, r.FreshnessQuery, r.StackID, r.NodeID, r.For, r.RecoveryFor, r.MaxAge})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// advance never resolves an incident without fresh, positive recovery evidence.
func advance(r Rule, old State, observation *Observation, now time.Time) State {
	next := old
	next.RuleHash = ruleHash(r)
	next.LastEvaluated = now
	if old.RuleHash != next.RuleHash || now.Sub(old.LastEvaluated) > 3*Interval {
		next.PendingSince, next.RecoverySince = time.Time{}, time.Time{}
	}
	if observation == nil || observation.At.IsZero() || observation.At.After(now) || now.Sub(observation.At) > r.MaxAge || math.IsNaN(observation.Value) || math.IsInf(observation.Value, 0) || observation.Firing == observation.Recovered {
		next.Status = "unknown"
		next.PendingSince, next.RecoverySince = time.Time{}, time.Time{}
		return next
	}
	next.Labels = observation.Labels
	if !observation.At.After(old.LastObserved) {
		// Re-reading the same sample cannot prove a sustained condition or recovery.
		return next
	}
	if !old.LastObserved.IsZero() && observation.At.Sub(old.LastObserved) > r.MaxAge {
		next.PendingSince, next.RecoverySince = time.Time{}, time.Time{}
	}
	next.LastObserved = observation.At
	value := observation.Value
	next.Value = &value
	if observation.Firing {
		next.RecoverySince = time.Time{}
		if old.AlertID != "" {
			next.Status = "firing"
			return next
		}
		if next.PendingSince.IsZero() {
			next.PendingSince = now
		}
		next.Status = "pending"
		if now.Sub(next.PendingSince) >= r.For {
			next.Status = "firing"
		}
		return next
	}
	next.PendingSince = time.Time{}
	if old.AlertID == "" {
		next.Status = "inactive"
		next.RecoverySince = time.Time{}
		return next
	}
	if next.RecoverySince.IsZero() {
		next.RecoverySince = now
	}
	next.Status = "firing"
	if now.Sub(next.RecoverySince) >= r.RecoveryFor {
		next.Status = "resolved"
	}
	return next
}
