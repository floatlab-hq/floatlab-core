package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Snapshot name format constants per the FloatLab naming convention.
const (
	PrefixUser        = "user"
	PrefixScheduled   = "flsnap"
	PrefixReplication = "fsrepl"
)

// SnapshotType classifies a snapshot by its name prefix.
type SnapshotType string

const (
	SnapshotTypeUser        SnapshotType = "user"
	SnapshotTypeScheduled   SnapshotType = "scheduled"
	SnapshotTypeReplication SnapshotType = "replication"
)

// UserSnapshotName returns a name for a user-created snapshot.
// Format: user-YYYYMMDD-HHMMSS-<label>
func UserSnapshotName(label string) string {
	return fmt.Sprintf("%s-%s-%s", PrefixUser, time.Now().UTC().Format("20060102-150405"), label)
}

// ScheduledSnapshotName returns a name for a scheduled snapshot.
// Format: flsnap-YYYYMMDD-HHMMSS-<type>  where type is hourly/daily/weekly/monthly
func ScheduledSnapshotName(snapType string) string {
	return fmt.Sprintf("%s-%s-%s", PrefixScheduled, time.Now().UTC().Format("20060102-150405"), snapType)
}

// ReplicationSnapshotName returns a name for a replication snapshot.
// Format: fsrepl-<seq>-YYYYMMDD-HHMMSS
func ReplicationSnapshotName(seq int) string {
	return fmt.Sprintf("%s-%06d-%s", PrefixReplication, seq, time.Now().UTC().Format("20060102-150405"))
}

// ClassifySnapshot returns the type of snapshot from its name.
func ClassifySnapshot(name string) SnapshotType {
	switch {
	case strings.HasPrefix(name, PrefixUser+"-"):
		return SnapshotTypeUser
	case strings.HasPrefix(name, PrefixScheduled+"-"):
		return SnapshotTypeScheduled
	case strings.HasPrefix(name, PrefixReplication+"-"):
		return SnapshotTypeReplication
	default:
		return SnapshotTypeUser
	}
}

// DatasetPath returns the canonical ZFS dataset path for a stack.
// Volume structure: floatlab/<stack-name>/<service>/<volume-name>
func DatasetPath(pool, stackName string) string {
	return fmt.Sprintf("%s/%s", pool, stackName)
}

func VolumeDatasetPath(pool, stackName, service, volume string) string {
	return fmt.Sprintf("%s/%s/%s/%s", pool, stackName, service, volume)
}

// ApplyRetention deletes scheduled snapshots of snapType beyond the keep limit.
// snapType must be "hourly", "daily", "weekly", or "monthly".
// Snapshots are sorted descending (lexicographic == chronological for this date format)
// so the newest `keep` snapshots are preserved and the rest are destroyed.
func ApplyRetention(ctx context.Context, zfs ZFSStore, dataset, snapType string, keep int) error {
	snaps, err := zfs.SnapshotList(ctx, dataset)
	if err != nil {
		return fmt.Errorf("store: retention %s %s: list: %w", dataset, snapType, err)
	}
	prefix := PrefixScheduled + "-"
	suffix := "-" + snapType
	var matching []SnapshotInfo
	for _, s := range snaps {
		if strings.HasPrefix(s.Name, prefix) && strings.HasSuffix(s.Name, suffix) {
			matching = append(matching, s)
		}
	}
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].Name > matching[j].Name
	})
	for i := keep; i < len(matching); i++ {
		if err := zfs.SnapshotDestroy(ctx, matching[i].Dataset, matching[i].Name); err != nil {
			return fmt.Errorf("store: retention cleanup %s@%s: %w", matching[i].Dataset, matching[i].Name, err)
		}
	}
	return nil
}
