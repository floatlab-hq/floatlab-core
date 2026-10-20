package control

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/pkg/alerts"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

func (s *Server) runAlertEvaluator(ctx context.Context) {
	evaluator := alerts.Evaluator{DB: s.db, Metrics: s.vmets, Broker: s.broker, IsLeader: func() bool { return s.raft != nil && s.raft.IsLeader() }}
	ticker := time.NewTicker(alerts.Interval)
	defer ticker.Stop()
	for {
		if evaluator.IsLeader() {
			// Each query is bounded separately so a slow pass cannot starve later rules.
			rules, err := s.alertRules(ctx)
			if err == nil {
				err = evaluator.Tick(ctx, rules, time.Now().UTC())
			}
			if err != nil && ctx.Err() == nil {
				s.log.Warn("alert evaluation failed", zap.Error(err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) alertRules(ctx context.Context) ([]alerts.Rule, error) {
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id,name,condition,recovery_condition,severity,COALESCE(stack_id,''),COALESCE(node_id,''),for_duration,recovery_duration,max_sample_age,annotations,enabled,freshness_query FROM alert_rules`})
	if err != nil {
		return nil, err
	}
	rules := make([]alerts.Rule, 0, len(rows.Values))
	for _, row := range rows.Values {
		rule := alerts.Rule{ID: str(row[0]), Name: str(row[1]), Condition: str(row[2]), RecoveryCondition: str(row[3]), Severity: str(row[4]), StackID: str(row[5]), NodeID: str(row[6]), Enabled: number(row[11]) != 0, FreshnessQuery: str(row[12])}
		annotations := map[string]string{}
		if err := json.Unmarshal([]byte(str(row[10])), &annotations); err != nil {
			rule.Condition = ""
		}
		rule.Message = annotations["description"]
		if duration, err := ruleDuration(str(row[7]), true); err == nil {
			rule.For = duration
		} else {
			rule.For = -1
		}
		if duration, err := ruleDuration(str(row[8]), true); err == nil {
			rule.RecoveryFor = duration
		} else {
			rule.RecoveryFor = -1
		}
		if duration, err := ruleDuration(str(row[9]), false); err == nil {
			rule.MaxAge = duration
		}
		rules = append(rules, rule)
	}
	rows, err = s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT r.id,r.name,r.metric,COALESCE(r.selector,''),r.comparator,r.threshold,r.duration,r.severity,r.message,r.stack_id,r.active,s.zfs_dataset,COALESCE(rt.active_node_id,s.primary_node_id),s.compose_yaml,s.name FROM stack_alert_rules r JOIN stacks s ON s.id=r.stack_id LEFT JOIN stack_runtime rt ON rt.stack_id=s.id WHERE rt.deleted_at IS NULL`})
	if err != nil {
		return nil, err
	}
	for _, row := range rows.Values {
		rule := alerts.Rule{ID: str(row[0]), Name: str(row[1]), Severity: str(row[7]), Message: str(row[8]), StackID: str(row[9]), NodeID: str(row[12]), Enabled: number(row[10]) != 0, Compose: true, MaxAge: alerts.DefaultMaxAge, RecoveryFor: time.Minute}
		if duration, err := ruleDuration(str(row[6]), true); err == nil {
			rule.For = duration
		} else {
			rule.For = -1
		}
		metric, selector := str(row[2]), str(row[3])
		dataset := str(row[11])
		if strings.HasPrefix(metric, "managed_volume_") {
			var err error
			dataset, err = managedAlertDatasets(str(row[13]), str(row[14]), selector)
			if err != nil {
				rules = append(rules, rule)
				continue
			}
		}
		query, freshness, err := composeAlertQuery(metric, selector, rule.StackID, rule.NodeID, dataset)
		if err != nil {
			rules = append(rules, rule)
			continue
		}
		operators := map[string]string{"gt": ">", "gte": ">=", "lt": "<", "lte": "<=", "eq": "=="}
		inverse := map[string]string{"gt": "<=", "gte": "<", "lt": ">=", "lte": ">", "eq": "!="}
		op := str(row[4])
		if operators[op] == "" {
			rules = append(rules, rule)
			continue
		}
		threshold := strconv.FormatFloat(row[5].(float64), 'g', -1, 64)
		rule.FreshnessQuery = freshness
		rule.Condition = "(" + query + ") " + operators[op] + " " + threshold
		rule.RecoveryCondition = "(" + query + ") " + inverse[op] + " " + threshold
		rules = append(rules, rule)
	}
	return rules, nil
}

func composeAlertQuery(metric, selector, stackID, nodeID, dataset string) (string, string, error) {
	labels := `stack_id=` + strconv.Quote(stackID) + `,node_id=` + strconv.Quote(nodeID)
	if strings.HasPrefix(metric, "container_") && selector != "" {
		labels += `,service=` + strconv.Quote(selector)
	}
	group := ` by(stack_id,node_id,service,replica)`
	freshness := func(name string) string { return `min` + group + `(timestamp(` + name + `{` + labels + `}))` }
	switch metric {
	case "container_cpu_percent":
		return `sum by(stack_id,node_id,service,replica)(rate(container_cpu_usage_seconds_total{` + labels + `}[5m])) * 100`, freshness("container_cpu_usage_seconds_total"), nil
	case "container_memory_percent":
		return `100 * sum by(stack_id,node_id,service,replica)(container_memory_usage_bytes{` + labels + `}) / (sum by(stack_id,node_id,service,replica)(container_memory_limit_bytes{` + labels + `}) > 0)`, freshness("container_memory_usage_bytes"), nil
	case "container_restart_count":
		return `sum by(stack_id,node_id,service,replica)(container_restart_count{` + labels + `})`, freshness("container_restart_count"), nil
	case "managed_volume_used_percent", "managed_volume_free_bytes":
		labels = `node_id=` + strconv.Quote(nodeID) + `,dataset=~` + strconv.Quote(dataset)
		query := `fl_zfs_dataset_available_bytes{` + labels + `}`
		if metric == "managed_volume_used_percent" {
			query = `100 * fl_zfs_dataset_used_bytes{` + labels + `} / ((fl_zfs_dataset_used_bytes{` + labels + `} + fl_zfs_dataset_available_bytes{` + labels + `}) > 0)`
		}
		return `label_set((` + query + `), "stack_id", ` + strconv.Quote(stackID) + `)`, `label_set(timestamp(fl_zfs_dataset_available_bytes{` + labels + `}), "stack_id", ` + strconv.Quote(stackID) + `)`, nil
	default:
		return "", "", fmt.Errorf("unsupported Compose alert metric %q", metric)
	}
}

func managedAlertDatasets(source, name, selector string) (string, error) {
	spec, err := compose.ParseLifecycle(source, name)
	if err != nil {
		return "", err
	}
	var datasets []string
	for _, mount := range spec.Mounts {
		if selector == "" || selector == mount.Source || selector == mount.Target || selector == mount.Dataset {
			datasets = append(datasets, regexp.QuoteMeta(mount.Dataset))
		}
	}
	if len(datasets) == 0 {
		return "", fmt.Errorf("no managed mounts match %q", selector)
	}
	sort.Strings(datasets)
	return "^(?:" + strings.Join(datasets, "|") + ")$", nil
}

func ruleDuration(value string, allowZero bool) (time.Duration, error) {
	if value == "" && allowZero {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration < 0 || (!allowZero && duration == 0) {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return duration, nil
}

func str(value interface{}) string { text, _ := value.(string); return text }
