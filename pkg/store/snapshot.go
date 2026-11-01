package store

import (
	"fmt"
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

// ClassifySnapshot returns the type of snapshot from its name.
func ClassifySnapshot(name string) SnapshotType {
	switch {
	case IsAutomatedSnapshot(name):
		return SnapshotTypeScheduled
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
