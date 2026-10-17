package control

import (
	"context"
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
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT (SELECT count(*) FROM alerts),(SELECT count(*) FROM notifications)`})
	if err != nil || len(rows.Values) != 1 || rows.Values[0][0] != float64(1) || rows.Values[0][1] != float64(1) {
		t.Fatalf("deduplication: %+v, %v", rows, err)
	}
	result.Status = "compatible"
	check()
	rows, err = db.Query(ctx, rqlite.Statement{SQL: `SELECT state FROM alerts`})
	if err != nil || rows.Values[0][0] != "resolved" {
		t.Fatalf("resolution: %+v, %v", rows, err)
	}
	result.Status = "incompatible"
	check()
	select {
	case <-events:
	default:
		t.Fatal("recurrence did not notify")
	}
}
