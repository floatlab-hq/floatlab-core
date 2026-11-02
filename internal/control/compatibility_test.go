package control

import (
	"context"
	"sync"
	"testing"

	"github.com/floatlab/floatlab-core/internal/testdb"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestCompatibilityAlertLifecycle(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	broker := notify.NewBroker()
	events, cancel := broker.Subscribe()
	defer cancel()
	result := release.Compatibility{Status: "incompatible", HostVersion: "0.2.0", Required: release.RequiredHost}
	check := func() {
		t.Helper()
		if err := recordHostCompatibility(ctx, db, broker, "node", result); err != nil {
			t.Fatal(err)
		}
	}
	check()
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT json_extract(result_json,'$.observer') FROM monitoring_observations`})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != "node" {
		t.Fatalf("missing local observer: %+v %v", rows, err)
	}
	select {
	case <-events:
	default:
		t.Fatal("no notification event")
	}
	check()
	select {
	case <-events:
		t.Fatal("duplicate notification")
	default:
	}
	result.Status = "unknown"
	result.Error = "unreachable"
	check()
	rows, err = db.Query(ctx, rqlite.Statement{SQL: `SELECT (SELECT count(*) FROM alerts),(SELECT count(*) FROM notifications)`})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != float64(1) || rows.Values[0][1] != float64(1) {
		t.Fatalf("deduplication: %+v, %v", rows, err)
	}
	rows, err = db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT json_extract(state_json,'$.status'),json_extract(state_json,'$.last_success') FROM monitoring_checks`})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != "unknown" || rows.Values[0][1] != "0001-01-01T00:00:00Z" {
		t.Fatalf("unknown compatibility claimed success: %+v %v", rows, err)
	}
	result.Status = "compatible"
	check()
	rows, err = db.Query(ctx, rqlite.Statement{SQL: `SELECT state FROM alerts`})
	if err != nil || rows.Values[0][0] != "resolved" {
		t.Fatalf("resolution: %+v, %v", rows, err)
	}
	result.Status = "incompatible"
	check()
	rows, err = db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM alerts`})
	if err != nil || len(rows.Values) != 2 {
		t.Fatalf("recurrence overwrote compatibility history: %+v %v", rows, err)
	}
	select {
	case <-events:
	default:
		t.Fatal("recurrence did not notify")
	}
}

func TestConcurrentCompatibilityChecksNotifyOncePerOccurrence(t *testing.T) {
	db := testdb.Start(t)
	ctx := context.Background()
	if err := rqlite.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	broker := notify.NewBroker()
	events, cancel := broker.Subscribe()
	defer cancel()
	incompatible := release.Compatibility{Status: "incompatible", HostVersion: "0.2.0", Required: release.RequiredHost}
	checkConcurrently := func() {
		t.Helper()
		start := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := recordHostCompatibility(ctx, db, broker, "node", incompatible); err != nil {
					t.Error(err)
				}
			}()
		}
		close(start)
		wg.Wait()
	}
	checkConcurrently()
	if len(events) != 1 {
		t.Fatalf("first occurrence broker deliveries: %d", len(events))
	}
	<-events
	if err := recordHostCompatibility(ctx, db, broker, "node", release.Compatibility{Status: "compatible"}); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("recovery broker deliveries: %d", len(events))
	}
	<-events
	checkConcurrently()
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT (SELECT count(*) FROM alerts),(SELECT count(*) FROM alerts WHERE state='active'),(SELECT count(*) FROM notifications)`})
	if err != nil {
		t.Fatal(err)
	}
	if got := rows.Values[0]; got[0] != float64(2) || got[1] != float64(1) || got[2] != float64(3) || len(events) != 1 {
		t.Fatalf("compatibility recurrence duplicated or lost: rows=%v events=%d", got, len(events))
	}
}
