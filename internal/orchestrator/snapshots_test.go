package orchestrator

import (
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/run"
)

func TestSnapshotDueCalendarBoundaries(t *testing.T) {
	mondayMonthStart := time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC)
	for _, interval := range []string{"1h", "1d", "1w", "1mo"} {
		if !snapshotDue(mondayMonthStart, interval) {
			t.Fatalf("%s not due at boundary", interval)
		}
	}
	if snapshotDue(mondayMonthStart.Add(time.Minute), "1h") {
		t.Fatal("hourly snapshot due off boundary")
	}
}

func TestProtectionKeepsPrimaryDataScheduledDuringManualStop(t *testing.T) {
	for _, tc := range []struct {
		instance run.StackInstance
		want     bool
	}{
		{run.StackInstance{NodeID: "primary", State: run.StateStopping, PreviousState: run.StateRunningPrimary}, true},
		{run.StackInstance{NodeID: "primary", State: run.StateIdle}, true},
		{run.StackInstance{NodeID: "backup", State: run.StateIdle}, false},
		{run.StackInstance{State: run.StateStopping, PreviousState: run.StateRunningBackup}, false},
		{run.StackInstance{NodeID: "primary", State: run.StateRestoring}, false},
	} {
		if got := scheduledProtectionState(&tc.instance, "primary"); got != tc.want {
			t.Errorf("state %+v eligible = %v, want %v", tc.instance, got, tc.want)
		}
	}
}
