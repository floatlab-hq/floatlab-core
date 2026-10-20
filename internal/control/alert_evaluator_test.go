package control

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestLoadBothRuleStores(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := config.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO alert_rules(id,name,type,condition,severity,recovery_condition,freshness_query) VALUES('global','RAM','metrics','used > 90','warning','used < 85','timestamp(used)')`},
		{SQL: `INSERT INTO stacks(id,name,primary_node_id,zfs_dataset,created_at,updated_at) VALUES('stack','web','primary','floatlab/web','','')`},
		{SQL: `INSERT INTO stack_runtime(stack_id,active_node_id,updated_at) VALUES('stack','backup','')`},
		{SQL: `INSERT INTO stack_alert_rules(id,stack_id,name,metric,selector,comparator,threshold,duration,severity,message) VALUES('compose','stack','CPU','container_cpu_percent','app','gt',95.5,'30s','warning','CPU high')`},
	}); err != nil {
		t.Fatal(err)
	}
	s := Server{db: db}
	rules, err := s.alertRules(ctx)
	if err != nil || len(rules) != 2 {
		t.Fatalf("%+v %v", rules, err)
	}
	if rules[0].RecoveryCondition != "used < 85" || rules[0].FreshnessQuery != "timestamp(used)" || rules[0].MaxAge != 2*time.Minute {
		t.Fatal(rules[0])
	}
	if !strings.Contains(rules[1].Condition, `node_id="backup"`) || !strings.Contains(rules[1].Condition, "> 95.5") || !strings.Contains(rules[1].RecoveryCondition, "<= 95.5") || rules[1].FreshnessQuery == "" {
		t.Fatal(rules[1])
	}
}

func TestManagedAlertMountSelectors(t *testing.T) {
	source := "name: web\nservices:\n  app:\n    image: busybox\n    volumes:\n      - data:/data\nvolumes:\n  data: {}\n"
	for _, selector := range []string{"", "data", "/data", "floatlab/web/data"} {
		pattern, err := managedAlertDatasets(source, "web", selector)
		if err != nil || pattern != "^(?:floatlab/web/data)$" {
			t.Fatalf("selector %q: %s %v", selector, pattern, err)
		}
	}
	if _, err := managedAlertDatasets(source, "web", "missing"); err == nil {
		t.Fatal("unknown mount accepted")
	}
}
