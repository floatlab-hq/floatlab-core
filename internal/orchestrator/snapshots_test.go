package orchestrator

import (
	"testing"
	"time"
)

func TestSnapshotDueCalendarBoundaries(t *testing.T) {
	mondayMonthStart := time.Date(2027, time.February, 1, 0, 0, 0, 0, time.Local)
	for _, interval := range []string{"1h", "1d", "1w", "1mo"} {
		if !snapshotDue(mondayMonthStart, interval) {
			t.Fatalf("%s not due at boundary", interval)
		}
	}
	if snapshotDue(mondayMonthStart.Add(time.Minute), "1h") {
		t.Fatal("hourly snapshot due off boundary")
	}
}
