package control

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/alerts"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/go-chi/chi/v5"
)

//go:embed alert_rules_builtin.json
var builtInAlertCatalogue []byte

var builtInAlertRules = func() []builtInAlertRule {
	var rules []builtInAlertRule
	if err := json.Unmarshal(builtInAlertCatalogue, &rules); err != nil {
		panic(err)
	}
	seen := map[string]bool{}
	for _, rule := range rules {
		if rule.ID == "" || rule.Name == "" || rule.Description == "" || seen[rule.ID] {
			panic("invalid built-in alert rule " + rule.ID)
		}
		seen[rule.ID] = true
		if err := rule.validate(); err != nil {
			panic(fmt.Errorf("invalid built-in alert rule %s: %w", rule.ID, err))
		}
	}
	return rules
}()

type builtInAlertRule struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Severity         string             `json:"severity"`
	Query            string             `json:"query"`
	FreshnessQuery   string             `json:"freshness_query"`
	Thresholds       map[string]float64 `json:"thresholds"`
	Unit             string             `json:"unit"`
	Workload         bool               `json:"workload"`
	Enabled          bool               `json:"enabled"`
	ForDuration      string             `json:"for_duration"`
	CriticalDuration string             `json:"critical_for_duration"`
	RecoveryDuration string             `json:"recovery_duration"`
}

type alertRuleOverrides struct {
	Enabled          *bool              `json:"enabled,omitempty"`
	ForDuration      *string            `json:"for_duration,omitempty"`
	CriticalDuration *string            `json:"critical_for_duration,omitempty"`
	RecoveryDuration *string            `json:"recovery_duration,omitempty"`
	Thresholds       map[string]float64 `json:"thresholds,omitempty"`
}

func builtInAlertDefault(id string) (builtInAlertRule, bool) {
	for _, rule := range builtInAlertRules {
		if rule.ID == id {
			return rule, true
		}
	}
	return builtInAlertRule{}, false
}

func (rule builtInAlertRule) withOverrides(encoded string) (builtInAlertRule, error) {
	var overrides alertRuleOverrides
	if err := json.Unmarshal([]byte(encoded), &overrides); err != nil {
		return builtInAlertRule{}, err
	}
	if overrides.Enabled != nil {
		rule.Enabled = *overrides.Enabled
	}
	if overrides.ForDuration != nil {
		if rule.ForDuration == "" {
			return builtInAlertRule{}, fmt.Errorf("for_duration is not supported for this rule")
		}
		rule.ForDuration = *overrides.ForDuration
	}
	if overrides.CriticalDuration != nil {
		if rule.CriticalDuration == "" {
			return builtInAlertRule{}, fmt.Errorf("critical_for_duration is not supported for this rule")
		}
		rule.CriticalDuration = *overrides.CriticalDuration
	}
	if overrides.RecoveryDuration != nil {
		if rule.RecoveryDuration == "" {
			return builtInAlertRule{}, fmt.Errorf("recovery_duration is not supported for this rule")
		}
		rule.RecoveryDuration = *overrides.RecoveryDuration
	}
	if len(overrides.Thresholds) != 0 {
		thresholds := make(map[string]float64, len(rule.Thresholds))
		for name, value := range rule.Thresholds {
			thresholds[name] = value
		}
		for name, value := range overrides.Thresholds {
			if _, ok := thresholds[name]; !ok {
				return builtInAlertRule{}, fmt.Errorf("unknown threshold %q", name)
			}
			thresholds[name] = value
		}
		rule.Thresholds = thresholds
	}
	return rule, rule.validate()
}

func (rule builtInAlertRule) validate() error {
	if rule.Severity != "" && !validSeverity(rule.Severity) {
		return fmt.Errorf("invalid severity %q", rule.Severity)
	}
	for name, value := range map[string]string{"for_duration": rule.ForDuration, "critical_for_duration": rule.CriticalDuration, "recovery_duration": rule.RecoveryDuration} {
		if _, err := ruleDuration(value, true); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, value := range rule.Thresholds {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			return fmt.Errorf("threshold %s must be finite and nonnegative", name)
		}
		switch {
		case strings.HasSuffix(name, "_failures"):
			if value < 1 || value > 1e9 || math.Trunc(value) != value {
				return fmt.Errorf("threshold %s must be a positive integer", name)
			}
		case strings.HasSuffix(name, "_seconds") || rule.Unit == "seconds":
			if value >= float64(math.MaxInt64)/float64(time.Second) {
				return fmt.Errorf("threshold %s exceeds the supported duration", name)
			}
		case rule.Unit == "percent":
			if value > 100 {
				return fmt.Errorf("threshold %s must be within 0 and 100 percent", name)
			}
		case rule.Unit == "ratio":
			if value > 1 {
				return fmt.Errorf("threshold %s must be within 0 and 1", name)
			}
		}
	}
	thresholds := rule.Thresholds
	if maxAge, warnAge := thresholds["max_age_seconds"], thresholds["warning_age_seconds"]; maxAge > 0 && warnAge >= maxAge {
		return fmt.Errorf("warning_age_seconds must be below max_age_seconds")
	}
	if rule.Query != "" {
		warning, critical := thresholds["warning"], thresholds["critical"]
		warningRecovery, criticalRecovery := thresholds["warning_recovery"], thresholds["critical_recovery"]
		if warning > critical || warningRecovery > warning || criticalRecovery > critical || warningRecovery > criticalRecovery {
			return fmt.Errorf("warning and recovery thresholds must not exceed their corresponding critical or firing thresholds")
		}
		return rule.metricRule().Validate()
	}
	return nil
}

func (rule builtInAlertRule) metricRule() alerts.Rule {
	warningFor, _ := ruleDuration(rule.ForDuration, true)
	criticalFor, _ := ruleDuration(rule.CriticalDuration, true)
	recoveryFor, _ := ruleDuration(rule.RecoveryDuration, true)
	return alerts.Rule{
		ID: rule.ID, Name: rule.Name, Message: rule.Description, Enabled: rule.Enabled, Severity: "warning",
		Condition: rule.queryThreshold(">", "warning"), RecoveryCondition: rule.queryThreshold("<", "warning_recovery"),
		FreshnessQuery: rule.FreshnessQuery, For: warningFor, RecoveryFor: recoveryFor, MaxAge: alerts.DefaultMaxAge,
		Compose: rule.Workload,
		Bands: &alerts.Bands{
			Query:    rule.Query,
			Unit:     rule.Unit,
			Warning:  alerts.Band{Trigger: rule.Thresholds["warning"], Recovery: rule.Thresholds["warning_recovery"], For: warningFor, RecoveryFor: recoveryFor},
			Critical: alerts.Band{Trigger: rule.Thresholds["critical"], Recovery: rule.Thresholds["critical_recovery"], For: criticalFor, RecoveryFor: recoveryFor},
		},
	}
}

func (rule builtInAlertRule) queryThreshold(operator, threshold string) string {
	return "(" + rule.Query + ") " + operator + " " + strconv.FormatFloat(rule.Thresholds[threshold], 'g', -1, 64)
}

func (s *Server) builtInAlertRule(ctx context.Context, id string) (builtInAlertRule, error) {
	rule, ok := builtInAlertDefault(id)
	if !ok {
		return builtInAlertRule{}, fmt.Errorf("unknown built-in alert rule %q", id)
	}
	result, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT overrides FROM alert_rule_overrides WHERE id=?`, Params: []interface{}{id}})
	if err != nil || len(result.Values) == 0 {
		return rule, err
	}
	return rule.withOverrides(str(result.Values[0][0]))
}

func (s *Server) metricBuiltInAlerts(ctx context.Context) ([]builtInAlertRule, error) {
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id,overrides FROM alert_rule_overrides`})
	if err != nil {
		return nil, err
	}
	overrides := map[string]string{}
	for _, row := range rows.Values {
		overrides[str(row[0])] = str(row[1])
	}
	var rules []builtInAlertRule
	for _, rule := range builtInAlertRules {
		if rule.Query == "" {
			continue
		}
		if encoded, ok := overrides[rule.ID]; ok {
			rule, err = rule.withOverrides(encoded)
			if err != nil {
				return nil, err
			}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func (s *Server) builtInAlertObjects(r *http.Request, filter string) ([]map[string]interface{}, error) {
	var ids []string
	for _, rule := range builtInAlertRules {
		if filter == "" || filter == rule.ID {
			ids = append(ids, rule.ID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	encodedIDs, _ := json.Marshal(ids)
	rows, err := s.queryObjects(r, `WITH rules AS (SELECT value AS id FROM json_each(?))
		SELECT rules.id,o.overrides,o.updated_at,(`+alertRuleEvaluations+` rules.id) AS evaluations,
		(SELECT json_group_array(json_object(
			'labels',json_extract(state_json,'$.labels'),
			'state',CASE json_extract(state_json,'$.status')
				WHEN 'healthy' THEN 'inactive'
				WHEN 'failing' THEN CASE WHEN json_extract(state_json,'$.incident_id') IS NULL THEN 'pending' ELSE 'firing' END
				ELSE json_extract(state_json,'$.status') END,
			'observed_at',json_extract(state_json,'$.observed_at'),
			'evaluated_at',json_extract(state_json,'$.evaluated_at'),
			'evidence',json_extract(state_json,'$.evidence'),
			'reason',json_extract(state_json,'$.reason')
		)) FROM monitoring_checks WHERE json_extract(state_json,'$.labels.rule_id')=rules.id) AS native_evaluations
		FROM rules LEFT JOIN alert_rule_overrides o ON o.id=rules.id`, string(encodedIDs))
	if err != nil {
		return nil, err
	}
	objects := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		rule, _ := builtInAlertDefault(str(row["id"]))
		encoded := str(row["overrides"])
		if encoded != "" {
			rule, err = rule.withOverrides(encoded)
			if err != nil {
				return nil, err
			}
		}
		condition, recovery := rule.Description, ""
		if rule.Query != "" {
			condition = rule.queryThreshold(">", "warning")
			recovery = rule.queryThreshold("<", "warning_recovery")
		}
		severity := rule.Severity
		if severity == "" {
			severity = "warning"
		}
		object := map[string]interface{}{
			"id": rule.ID, "name": rule.Name, "condition": condition, "severity": severity,
			"enabled": rule.Enabled, "built_in": true, "overridden": encoded != "",
			"thresholds": rule.Thresholds, "threshold_unit": rule.Unit, "annotations": map[string]string{"description": rule.Description},
			"recovery_condition": recovery, "freshness_query": rule.FreshnessQuery,
			"evaluations": append(ruleEvaluations(row["evaluations"]), ruleEvaluations(row["native_evaluations"])...),
		}
		for name, value := range map[string]string{"for_duration": rule.ForDuration, "critical_for_duration": rule.CriticalDuration, "recovery_duration": rule.RecoveryDuration} {
			if value != "" {
				object[name] = value
			}
		}
		if row["updated_at"] != nil {
			object["updated_at"] = row["updated_at"]
		}
		objects = append(objects, object)
	}
	return objects, nil
}

func (s *Server) handleBuiltInAlertUpdate(w http.ResponseWriter, r *http.Request, rule builtInAlertRule, changes map[string]json.RawMessage) {
	for name, value := range changes {
		switch name {
		case "enabled", "thresholds", "for_duration", "critical_for_duration", "recovery_duration":
			if string(value) == "null" {
				writeError(w, 400, name+" must not be null")
				return
			}
			if strings.HasSuffix(name, "_duration") && string(value) == `""` {
				writeError(w, 400, name+" needs a duration such as 0s")
				return
			}
			if strings.HasSuffix(name, "_duration") && rule.Query == "" && rule.ID != peerReachabilityRuleID {
				writeError(w, 400, name+" is not adjustable for this rule")
				return
			}
			if name == "thresholds" {
				var thresholds map[string]*float64
				if err := json.Unmarshal(value, &thresholds); err != nil {
					writeError(w, 400, "thresholds must be a numeric object")
					return
				}
				for threshold, value := range thresholds {
					if value == nil {
						writeError(w, 400, threshold+" must be a number")
						return
					}
				}
			}
		default:
			writeError(w, 400, name+" is not editable for built-in rules")
			return
		}
	}
	var parsed alertRuleOverrides
	patch, _ := json.Marshal(changes)
	if len(changes) == 0 || json.Unmarshal(patch, &parsed) != nil {
		writeError(w, 400, "invalid built-in rule override")
		return
	}
	for attempt := 0; attempt < 4; attempt++ {
		rows, err := s.db.QueryStrong(r.Context(), rqlite.Statement{SQL: `SELECT COALESCE(overrides,'{}'),json_patch(COALESCE(overrides,'{}'),?),overrides
			FROM (SELECT ? AS rule_id) LEFT JOIN alert_rule_overrides ON id=rule_id`, Params: []interface{}{string(patch), rule.ID}})
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		old, next, exists := str(rows.Values[0][0]), str(rows.Values[0][1]), rows.Values[0][2] != nil
		if _, err := rule.withOverrides(next); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		statement := rqlite.Statement{SQL: `INSERT OR IGNORE INTO alert_rule_overrides(id,overrides,updated_at) VALUES(?,?,?)`, Params: []interface{}{rule.ID, next, now}}
		if exists {
			statement = rqlite.Statement{SQL: `UPDATE alert_rule_overrides SET overrides=?,updated_at=? WHERE id=? AND overrides=?`, Params: []interface{}{next, now, rule.ID, old}}
		}
		result, err := s.db.Request(r.Context(), statement)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		if result.RowsAffected > 0 {
			s.writeBuiltInAlert(w, r, rule.ID)
			return
		}
	}
	writeError(w, 409, "built-in rule changed concurrently")
}

func (s *Server) handleBuiltInAlertReset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, ok := builtInAlertDefault(id); !ok {
		writeError(w, 404, "built-in rule not found")
		return
	}
	if _, err := s.db.Request(r.Context(), rqlite.Statement{SQL: `DELETE FROM alert_rule_overrides WHERE id=?`, Params: []interface{}{id}}); err != nil {
		writeError(w, 500, err.Error())
		return
	}
	s.writeBuiltInAlert(w, r, id)
}

func (s *Server) writeBuiltInAlert(w http.ResponseWriter, r *http.Request, id string) {
	rows, err := s.builtInAlertObjects(r, id)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, rows[0])
}
