package store

import (
	"testing"
	"time"
)

func TestSnapshotTimestampRFDVectors(t *testing.T) {
	for _, tc := range []struct{ date, code string }{
		{"2025-01-01T00:00:00Z", "000000"},
		{"2026-01-01T00:00:00Z", "0A0SM0"},
		{"2026-10-28T00:00:00Z", "0J8F40"},
		{"2040-01-01T00:00:00Z", "4PEMR0"},
		{"2040-06-01T00:00:00Z", "4TM7R0"},
		{"2127-01-29T16:51:09Z", "ZZZZZZ"},
	} {
		instant, _ := time.Parse(time.RFC3339, tc.date)
		got, err := EncodeSnapshotTimestamp(instant)
		if err != nil || got != tc.code {
			t.Fatalf("encode %s = %q, %v", tc.date, got, err)
		}
		decoded, err := DecodeSnapshotTimestamp(tc.code)
		if err != nil || !decoded.Equal(instant) {
			t.Fatalf("decode %s = %v, %v", tc.code, decoded, err)
		}
	}
}

func TestSnapshotTimestampBoundariesAndAliases(t *testing.T) {
	epoch := time.Unix(snapshotEpoch, 0)
	for _, offset := range []time.Duration{0, time.Nanosecond, time.Second, 3*time.Second - time.Nanosecond} {
		got, err := EncodeSnapshotTimestamp(epoch.Add(offset))
		if err != nil || got != "000000" {
			t.Fatalf("tick %s = %q, %v", offset, got, err)
		}
	}
	if got, _ := EncodeSnapshotTimestamp(epoch.Add(3 * time.Second)); got != "000001" {
		t.Fatal(got)
	}
	for _, instant := range []time.Time{epoch.Add(-time.Nanosecond), time.Unix(snapshotEpoch+snapshotTicks*3, 0)} {
		if _, err := EncodeSnapshotTimestamp(instant); err == nil {
			t.Fatalf("accepted %v", instant)
		}
	}
	for _, input := range []string{"oooooi", "OOOOOL", "000001"} {
		got, err := DecodeSnapshotTimestamp(input)
		if err != nil || !got.Equal(epoch.Add(3*time.Second)) {
			t.Fatalf("alias %q = %v, %v", input, got, err)
		}
	}
	for _, input := range []string{"00000", "0000000", "00000U", " 00000", "fl-000000", "é0000"} {
		if _, err := DecodeSnapshotTimestamp(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	prev := ""
	for _, tick := range []int64{0, 1, 9, 10, 31, 32, 1023, 1024, snapshotTicks - 1} {
		got, err := EncodeSnapshotTimestamp(time.Unix(snapshotEpoch+tick*3, 0))
		if err != nil || got <= prev {
			t.Fatalf("ordering %q <= %q: %v", got, prev, err)
		}
		prev = got
	}
}
