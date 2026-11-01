package store

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type RetentionTier struct {
	Interval string `json:"interval"`
	Retain   int    `json:"retain"`
}

var policyToken = regexp.MustCompile(`^([1-9][0-9]*)?(mo|m|h|d|w|y)/([1-9][0-9]*)$`)
var policyInterval = regexp.MustCompile(`^([1-9][0-9]*)(mo|m|h|d|w|y)$`)

func ParseSnapshotPolicy(policy string) ([]RetentionTier, error) {
	var tiers []RetentionTier
	seen := map[string]bool{}
	for _, token := range strings.Fields(policy) {
		match := policyToken.FindStringSubmatch(token)
		if match == nil || seen[match[2]] {
			return nil, fmt.Errorf("invalid or duplicate snapshot tier %q", token)
		}
		countText := match[1]
		if countText == "" {
			countText = "1"
		}
		count, err := strconv.Atoi(countText)
		if err != nil {
			return nil, fmt.Errorf("invalid snapshot interval %q", token)
		}
		retain, err := strconv.Atoi(match[3])
		if err != nil || retain > math.MaxInt/count {
			return nil, fmt.Errorf("snapshot retention overflows: %q", token)
		}
		tier := RetentionTier{Interval: countText + match[2], Retain: retain}
		if _, err := retentionCutoff(time.Now().UTC(), tier); err != nil {
			return nil, err
		}
		seen[match[2]] = true
		tiers = append(tiers, tier)
	}
	return tiers, nil
}

func intervalParts(interval string) (int, string, error) {
	match := policyInterval.FindStringSubmatch(interval)
	if match == nil {
		return 0, "", fmt.Errorf("invalid snapshot interval %q", interval)
	}
	count, err := strconv.Atoi(match[1])
	if err != nil {
		return 0, "", err
	}
	return count, match[2], nil
}

func floorDiv(n, d int64) int64 {
	q := n / d
	if n%d < 0 {
		q--
	}
	return q
}

func intervalBucket(t time.Time, count int, unit string) int64 {
	t = t.UTC()
	switch unit {
	case "m":
		return floorDiv(floorDiv(t.Unix(), 60), int64(count))
	case "h":
		return floorDiv(floorDiv(t.Unix(), 3600), int64(count))
	case "d":
		return floorDiv(floorDiv(t.Unix(), 86400), int64(count))
	case "w":
		return floorDiv(floorDiv(t.Unix()-4*86400, 7*86400), int64(count))
	case "mo":
		return floorDiv(int64(t.Year())*12+int64(t.Month())-1, int64(count))
	case "y":
		return floorDiv(int64(t.Year()), int64(count))
	}
	return 0
}

// SnapshotDue compares adjacent UTC minutes, keeping multiday intervals stable across year boundaries.
func SnapshotDue(now time.Time, interval string) bool {
	count, unit, err := intervalParts(interval)
	if err != nil {
		return false
	}
	now = now.UTC().Truncate(time.Minute)
	return intervalBucket(now, count, unit) != intervalBucket(now.Add(-time.Minute), count, unit)
}

func retentionCutoff(now time.Time, tier RetentionTier) (time.Time, error) {
	count, unit, err := intervalParts(tier.Interval)
	if err != nil {
		return time.Time{}, err
	}
	if tier.Retain < 1 || tier.Retain > math.MaxInt/count {
		return time.Time{}, fmt.Errorf("invalid snapshot retention")
	}
	span := int64(count * tier.Retain)
	seconds := map[string]int64{"m": 60, "h": 3600, "d": 86400, "w": 7 * 86400}
	if scale, ok := seconds[unit]; ok {
		if span > math.MaxInt64/int64(time.Second)/scale {
			return time.Time{}, fmt.Errorf("snapshot retention duration overflows")
		}
		return now.Add(-time.Duration(span*scale) * time.Second), nil
	}
	// Calendar windows clamp to the final valid day instead of AddDate's forward normalization.
	months := span
	if unit == "y" {
		if span > 9999 {
			return time.Time{}, fmt.Errorf("snapshot retention exceeds calendar range")
		}
		months *= 12
	}
	if months > 9999*12 {
		return time.Time{}, fmt.Errorf("snapshot retention exceeds calendar range")
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), time.UTC).AddDate(0, -int(months), 0)
	last := monthStart.AddDate(0, 1, 0).AddDate(0, 0, -1).Day()
	day := now.Day()
	if day > last {
		day = last
	}
	return monthStart.AddDate(0, 0, day-1), nil
}

// RetainedSnapshots selects the earliest point in each tier bucket within its time window.
// Callers must additionally preserve actual holds and reconcile FloatLab protection metadata.
func RetainedSnapshots(snaps []SnapshotInfo, tiers []RetentionTier, now time.Time) (map[string]bool, error) {
	now = now.UTC()
	keep := map[string]bool{}
	type point struct {
		snapshot SnapshotInfo
		created  time.Time
	}
	var points []point
	for _, snap := range snaps {
		if !IsAutomatedSnapshot(snap.Name) {
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, snap.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s@%s creation time: %w", snap.Dataset, snap.Name, err)
		}
		points = append(points, point{snap, created})
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].created.Equal(points[j].created) {
			return points[i].snapshot.Name < points[j].snapshot.Name
		}
		return points[i].created.Before(points[j].created)
	})
	latest := map[string]point{}
	for _, p := range points {
		latest[p.snapshot.Dataset] = p
		if p.created.After(now) {
			keep[p.snapshot.Dataset+"@"+p.snapshot.Name] = true
		}
	}
	for _, p := range latest {
		keep[p.snapshot.Dataset+"@"+p.snapshot.Name] = true
	}
	for _, tier := range tiers {
		cutoff, err := retentionCutoff(now, tier)
		if err != nil {
			return nil, err
		}
		count, unit, _ := intervalParts(tier.Interval)
		seen := map[string]map[int64]bool{}
		for _, p := range points {
			if !p.created.After(cutoff) || p.created.After(now) {
				continue
			}
			if seen[p.snapshot.Dataset] == nil {
				seen[p.snapshot.Dataset] = map[int64]bool{}
			}
			bucket := intervalBucket(p.created, count, unit)
			if !seen[p.snapshot.Dataset][bucket] {
				keep[p.snapshot.Dataset+"@"+p.snapshot.Name] = true
				seen[p.snapshot.Dataset][bucket] = true
			}
		}
	}
	return keep, nil
}
