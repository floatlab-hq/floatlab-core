package store

import (
	"fmt"
	"testing"
	"time"
)

func TestSnapshotPolicyParsing(t *testing.T) {
	for _, policy := range []string{"h/24 d/28", "1h/24 1d/28", "5m/6 2h/24 1w/4 1mo/12 1y/2", ""} {
		if _, err := ParseSnapshotPolicy(policy); err != nil {
			t.Fatalf("%q: %v", policy, err)
		}
	}
	for _, policy := range []string{"0h/24", "h/0", "h/24 2h/2", "x/4", "1h/-1", "1h/2garbage", "999999999999999999999999h/2", "1h/99999999999999999999999"} {
		if _, err := ParseSnapshotPolicy(policy); err == nil {
			t.Fatalf("accepted %q", policy)
		}
	}
}

func TestRetentionOverlappingHourlyDailyWindows(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var snaps []SnapshotInfo
	for hours := 30 * 24; hours >= 0; hours-- {
		at := now.Add(-time.Duration(hours) * time.Hour)
		name, _ := AutomatedSnapshotName(at)
		snaps = append(snaps, SnapshotInfo{Dataset: "tank/source", Name: name, CreatedAt: at.Format(time.RFC3339)})
	}
	tiers, _ := ParseSnapshotPolicy("h/24 d/28")
	keep, err := RetainedSnapshots(snaps, tiers, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, snap := range snaps {
		at, _ := time.Parse(time.RFC3339, snap.CreatedAt)
		age := now.Sub(at)
		want := age < 24*time.Hour || (age < 28*24*time.Hour && at.Hour() == 0) || age == 28*24*time.Hour-time.Hour
		// The oldest partial daily bucket keeps its earliest available point within the window.
		if got := keep[snap.Dataset+"@"+snap.Name]; got != want {
			t.Fatalf("age %v at %s: kept %v, want %v", age, at, got, want)
		}
	}
}

func TestRetentionMissedRunsLatestAndDatasets(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	var snaps []SnapshotInfo
	for _, dataset := range []string{"tank/a", "tank/b"} {
		for i, age := range []time.Duration{90 * 24 * time.Hour, 80 * 24 * time.Hour} {
			at := now.Add(-age)
			name, _ := AutomatedSnapshotName(at)
			snaps = append(snaps, SnapshotInfo{Dataset: dataset, Name: name, CreatedAt: at.Format(time.RFC3339), Used: int64(i)})
		}
	}
	snaps = append(snaps, SnapshotInfo{Dataset: "tank/a", Name: "user-old", CreatedAt: "bad legacy time"})
	tiers, _ := ParseSnapshotPolicy("d/28")
	keep, err := RetainedSnapshots(snaps, tiers, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(keep) != 2 {
		t.Fatalf("expired history should not fill missed daily slots: %v", keep)
	}
	for _, snap := range snaps {
		if snap.Used == 1 && !keep[snap.Dataset+"@"+snap.Name] {
			t.Fatal("lost latest", snap)
		}
	}
	snaps = append(snaps, SnapshotInfo{Dataset: "tank/a", Name: "fl-000000", CreatedAt: "bad time"})
	if _, err := RetainedSnapshots(snaps, tiers, now); err == nil {
		t.Fatal("malformed managed timestamp must block pruning")
	}
}

func TestSnapshotDueStableBoundaries(t *testing.T) {
	for _, tc := range []struct {
		date, interval string
		want           bool
	}{
		{"2026-11-02T00:00:00Z", "1w", true},
		{"2026-11-01T00:00:00Z", "1w", false},
		{"2026-11-01T12:00:00Z", "1h", true},
		{"2026-11-01T12:01:00Z", "1h", false},
		{"2026-11-01T00:00:00Z", "1mo", true},
		{"2026-11-02T00:00:00Z", "1mo", false},
		{"2026-11-01T12:05:00Z", "5m", true},
	} {
		at, _ := time.Parse(time.RFC3339, tc.date)
		if got := SnapshotDue(at, tc.interval); got != tc.want {
			t.Fatal(fmt.Sprintf("%s %s due=%v", tc.date, tc.interval, got))
		}
	}
}

func TestCalendarRetentionClampsMonthEnd(t *testing.T) {
	now := time.Date(2026, 3, 31, 10, 0, 0, 0, time.UTC)
	cutoff, err := retentionCutoff(now, RetentionTier{Interval: "1mo", Retain: 1})
	want := time.Date(2026, 2, 28, 10, 0, 0, 0, time.UTC)
	if err != nil || !cutoff.Equal(want) {
		t.Fatalf("cutoff %v, %v", cutoff, err)
	}
}
