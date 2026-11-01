package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const HoldTargetsProperty = "floatlab:hold-targets"
const TransferTargetsProperty = "floatlab:transfer-targets"
const TargetGenerationsProperty = "floatlab:target-generations"

var backupID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)
var backupPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
var backupLocks sync.Map
var activeBackupMu sync.Mutex
var activeBackups = map[string]map[*context.CancelFunc]int64{}

func registerBackup(ctx context.Context, key string, generation int64) (context.Context, func()) {
	child, cancel := context.WithCancel(ctx)
	activeBackupMu.Lock()
	if activeBackups[key] == nil {
		activeBackups[key] = map[*context.CancelFunc]int64{}
	}
	activeBackups[key][&cancel] = generation
	activeBackupMu.Unlock()
	return child, func() {
		cancel()
		activeBackupMu.Lock()
		delete(activeBackups[key], &cancel)
		if len(activeBackups[key]) == 0 {
			delete(activeBackups, key)
		}
		activeBackupMu.Unlock()
	}
}
func cancelBackups(key string, cutoff int64) {
	activeBackupMu.Lock()
	defer activeBackupMu.Unlock()
	for cancel, generation := range activeBackups[key] {
		if generation <= cutoff {
			(*cancel)()
		}
	}
}

func backupLock(dataset string) func() {
	lock, _ := backupLocks.LoadOrStore(dataset, &sync.Mutex{})
	lock.(*sync.Mutex).Lock()
	return lock.(*sync.Mutex).Unlock
}
func validBackupDataset(s string) bool {
	if s == "" {
		return false
	}
	for _, p := range strings.Split(s, "/") {
		if !backupPart.MatchString(p) || p == "." || p == ".." {
			return false
		}
	}
	return true
}

type ReplicationRequest struct {
	Generation                                                                        int64
	Dataset, Snapshot, SourceNodeID, DestNodeID, DestDataset, DestHost, Policy, JobID string
	DestPort                                                                          int
	IdentityFile, KnownHostsFile                                                      string
}
type BackupSnapshot struct {
	Name string
	GUID string
}
type BackupState struct {
	Snapshots   []BackupSnapshot
	ResumeToken string
}

// BackupTransport keeps stream handling separate from deterministic recovery decisions.
type BackupTransport interface {
	Source(context.Context, string) ([]BackupSnapshot, error)
	State(context.Context) (BackupState, error)
	Send(context.Context, []string) (int64, error)
	Abort(context.Context) error
	ValidateToken(context.Context, string) error
	Protect(context.Context, string, string, string, bool) error
	Prune(context.Context) error
}

// ReplicateBackup resumes the original stream before catching up to the requested point.
// Recovery follows modd-zfs-backup: inspect tokens first, match GUIDs, verify receipt, then advance holds.
func ReplicateBackup(ctx context.Context, r ReplicationRequest, t BackupTransport) (result *SendResult, err error) {
	if !validBackupDataset(r.Dataset) || !backupID.MatchString(r.DestNodeID) || !backupID.MatchString(r.SourceNodeID) || !backupPart.MatchString(r.Snapshot) {
		return nil, errors.New("invalid replication identity or snapshot")
	}
	unlock := backupLock(r.Dataset)
	defer unlock()
	source, err := t.Source(ctx, r.Dataset)
	if err != nil {
		return nil, err
	}
	target := -1
	for i, s := range source {
		if s.Name == r.Snapshot {
			target = i
		}
	}
	if target < 0 {
		return nil, errors.New("requested replication snapshot is missing")
	}
	state, err := t.State(ctx)
	if err != nil {
		return nil, err
	}
	protected := map[string]bool{}
	failedSend := false
	defer func() {
		if !failedSend || err == nil || ctx.Err() != nil || len(protected) == 0 {
			return
		}
		reconcile, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		current, e := t.State(reconcile)
		if e != nil || current.ResumeToken != "" {
			return
		}
		// A completed full seed may be the only common point after the next stream fails.
		base := newestCommonSnapshot(source, current.Snapshots)
		if len(current.Snapshots) > 0 && base < 0 {
			return
		}
		if base >= 0 {
			if e = t.Protect(reconcile, source[base].Name, r.DestNodeID, "hold", true); e != nil {
				err = errors.Join(err, e)
				return
			}
			for i, s := range source {
				if i != base {
					if e = t.Protect(reconcile, s.Name, r.DestNodeID, "hold", false); e != nil {
						err = errors.Join(err, e)
						return
					}
				}
			}
		}
		for snapshot := range protected {
			if e = t.Protect(reconcile, snapshot, r.DestNodeID, "transfer", false); e != nil {
				err = errors.Join(err, e)
				return
			}
		}
	}()
	result = &SendResult{JobID: r.JobID}
	if state.ResumeToken != "" {
		for _, s := range source {
			protected[s.Name] = true
		}
		if err = t.ValidateToken(ctx, state.ResumeToken); err != nil {
			// Abort only after source inspection succeeded; a transport failure must not retire receive state.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !errors.Is(err, ErrInvalidResumeToken) {
				return nil, err
			}
			if err = t.Abort(ctx); err != nil {
				return nil, err
			}
		} else {
			n, e := t.Send(ctx, []string{"send", "-t", state.ResumeToken})
			result.BytesSent += n
			if e != nil {
				failedSend = true
				return nil, e
			}
		}
		state, err = t.State(ctx)
		if err != nil {
			return nil, err
		}
		if state.ResumeToken != "" {
			return nil, errors.New("receive remains incomplete")
		}
	}
	base := newestCommonSnapshot(source, state.Snapshots)
	if len(state.Snapshots) > 0 && base < 0 {
		return nil, errors.New("backup has no common source snapshot; preserving destination history")
	}
	// A delayed retry must never move a destination recovery point backward.
	stale := base > target
	if stale {
		target = base
		r.Snapshot = source[target].Name
	}
	source = source[:target+1]
	start := base
	if start < 0 {
		start = 0
	}
	for _, s := range source[start:] {
		protected[s.Name] = true
		if err = t.Protect(ctx, s.Name, r.DestNodeID, "transfer", true); err != nil {
			return nil, err
		}
	}
	if base < target {
		if base < 0 {
			n, e := t.Send(ctx, []string{"send", r.Dataset + "@" + source[0].Name})
			result.BytesSent += n
			if e != nil {
				failedSend = true
				return nil, e
			}
			base = 0
		}
		if base < target {
			n, e := t.Send(ctx, []string{"send", "-I", r.Dataset + "@" + source[base].Name, r.Dataset + "@" + source[target].Name})
			result.BytesSent += n
			if e != nil {
				failedSend = true
				return nil, e
			}
		}
	}
	state, err = t.State(ctx)
	if err != nil {
		return nil, err
	}
	if state.ResumeToken != "" {
		return nil, errors.New("receive verification found pending resume state")
	}
	received := map[string]bool{}
	for _, s := range state.Snapshots {
		received[s.GUID] = true
	}
	for _, s := range source[start:] {
		if !received[s.GUID] {
			return nil, fmt.Errorf("destination snapshot GUID verification failed: %s", s.Name)
		}
	}
	if err = t.Protect(ctx, r.Snapshot, r.DestNodeID, "hold", true); err != nil {
		return nil, err
	}
	for _, s := range source {
		if s.Name != r.Snapshot {
			if err = t.Protect(ctx, s.Name, r.DestNodeID, "hold", false); err != nil {
				return nil, err
			}
		}
		if err = t.Protect(ctx, s.Name, r.DestNodeID, "transfer", false); err != nil {
			return nil, err
		}
	}
	if !stale {
		if err = t.Prune(ctx); err != nil {
			result.PruneError = err.Error()
		}
	}
	return result, nil
}

func newestCommonSnapshot(source, received []BackupSnapshot) int {
	base := -1
	for i, s := range source {
		for _, d := range received {
			if s.GUID == d.GUID {
				base = i
			}
		}
	}
	return base
}

var ErrInvalidResumeToken = errors.New("invalid resume token")

// UpdateSnapshotProtection keeps enforcement and descriptive properties in one serialized mutation.
// Adding writes the hold first; removing writes metadata first. A crash always fails closed.
func UpdateSnapshotProtection(ctx context.Context, dataset, snapshot, node, kind string, add bool) error {
	if !validBackupDataset(dataset) || !backupPart.MatchString(snapshot) || !backupID.MatchString(node) || (kind != "hold" && kind != "transfer") {
		return errors.New("invalid snapshot protection")
	}
	property := HoldTargetsProperty
	if kind == "transfer" {
		property = TransferTargetsProperty
	}
	target := dataset + "@" + snapshot
	tag := "floatlab:" + kind + ":" + node
	raw, err := run(ctx, "zfs", "get", "-H", "-o", "value", property, target)
	if err != nil {
		return err
	}
	var nodes []string
	if raw != "-" && raw != "" {
		if err = json.Unmarshal([]byte(raw), &nodes); err != nil {
			return fmt.Errorf("invalid %s metadata: %w", property, err)
		}
	}
	found := false
	next := []string{}
	for _, n := range nodes {
		if n == node {
			found = true
			if !add {
				continue
			}
		}
		next = append(next, n)
	}
	if add && !found {
		next = append(next, node)
	}
	sort.Strings(next)
	if add {
		held, e := snapshotHolds(ctx, target)
		if e != nil {
			return e
		}
		if !held[tag] {
			if _, err = run(ctx, "zfs", "hold", tag, target); err != nil {
				return err
			}
		}
	}
	encoded, _ := json.Marshal(next)
	if _, err = run(ctx, "zfs", "set", property+"="+string(encoded), target); err != nil {
		return err
	}
	if !add {
		held, e := snapshotHolds(ctx, target)
		if e != nil {
			return e
		}
		if held[tag] {
			_, err = run(ctx, "zfs", "release", tag, target)
		}
	}
	return err
}
func snapshotHolds(ctx context.Context, target string) (map[string]bool, error) {
	out, err := run(ctx, "zfs", "holds", "-H", target)
	if err != nil {
		return nil, err
	}
	tags := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			tags[f[1]] = true
		}
	}
	return tags, nil
}

// PruneSnapshots leaves legacy names and every actual or described hold untouched.
func PruneSnapshots(ctx context.Context, dataset, policy string) error {
	if !validBackupDataset(dataset) {
		return errors.New("invalid prune dataset")
	}
	unlockFile, err := lockBackupDataset(dataset)
	if err != nil {
		return err
	}
	defer unlockFile()
	unlock := backupLock(dataset)
	defer unlock()
	return pruneSnapshots(ctx, dataset, policy)
}
func pruneSnapshots(ctx context.Context, dataset, policy string) error {
	tiers, err := ParseSnapshotPolicy(policy)
	if err != nil {
		return err
	}
	snaps, err := New().SnapshotList(ctx, dataset)
	if err != nil {
		return err
	}
	keep, err := RetainedSnapshots(snaps, tiers, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.Dataset != dataset || !IsAutomatedSnapshot(s.Name) {
			continue
		}
		metadata := map[string]bool{}
		for _, node := range s.HoldTargets {
			metadata["floatlab:hold:"+node] = true
		}
		for _, node := range s.TransferTargets {
			metadata["floatlab:transfer:"+node] = true
		}
		actual := map[string]bool{}
		for _, tag := range s.Holds {
			actual[tag] = true
			if (strings.HasPrefix(tag, "floatlab:hold:") || strings.HasPrefix(tag, "floatlab:transfer:")) && !metadata[tag] {
				return fmt.Errorf("snapshot protection requires reconciliation: %s@%s", dataset, s.Name)
			}
		}
		for tag := range metadata {
			if !actual[tag] {
				return fmt.Errorf("snapshot protection requires reconciliation: %s@%s", dataset, s.Name)
			}
		}
		if keep[s.Dataset+"@"+s.Name] {
			continue
		}
		target := dataset + "@" + s.Name
		tags, e := snapshotHolds(ctx, target)
		if e != nil {
			return e
		}
		protected := len(tags) > 0
		for _, prop := range []string{HoldTargetsProperty, TransferTargetsProperty} {
			v, e := run(ctx, "zfs", "get", "-H", "-o", "value", prop, target)
			if e != nil {
				return e
			}
			if v != "-" && v != "" && v != "[]" {
				protected = true
			}
		}
		if !protected {
			clones, e := run(ctx, "zfs", "get", "-H", "-o", "value", "clones", target)
			if e != nil {
				return e
			}
			if clones != "-" && clones != "" {
				continue
			}
			if _, err = run(ctx, "zfs", "destroy", target); err != nil {
				return err
			}
		}
	}
	return nil
}
func CleanupBackup(ctx context.Context, dataset, node, policy string) error {
	return CleanupBackupGeneration(ctx, dataset, node, policy, 0)
}
func CleanupBackupGeneration(ctx context.Context, dataset, node, policy string, generation int64) error {
	if !validBackupDataset(dataset) || !backupID.MatchString(node) || generation < 0 {
		return errors.New("invalid backup cleanup")
	}
	cancelBackups(dataset+"@"+node, generation)
	unlockFile, err := lockBackupDataset(dataset)
	if err != nil {
		return err
	}
	defer unlockFile()
	unlock := backupLock(dataset)
	defer unlock()
	return cleanupBackupGenerationLocked(ctx, dataset, node, policy, generation)
}

func cleanupBackupGenerationLocked(ctx context.Context, dataset, node, policy string, generation int64) error {
	removed, err := removedBackupTargets(ctx, dataset)
	if err != nil {
		return err
	}
	if old, found := removed[node]; !found || old < generation {
		removed[node] = generation
	}
	encoded, _ := json.Marshal(removed)
	if _, err = run(ctx, "zfs", "set", "floatlab:removed-targets="+string(encoded), dataset); err != nil {
		return err
	}
	accepted, err := backupTargetGenerations(ctx, dataset, TargetGenerationsProperty)
	if err != nil {
		return err
	}
	if accepted[node] > generation {
		return nil
	}
	snaps, err := New().SnapshotList(ctx, dataset)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.Dataset != dataset {
			continue
		}
		for _, kind := range []string{"hold", "transfer"} {
			if err = UpdateSnapshotProtection(ctx, dataset, s.Name, node, kind, false); err != nil {
				return err
			}
		}
	}
	return pruneSnapshots(ctx, dataset, policy)
}

func removedBackupTargets(ctx context.Context, dataset string) (map[string]int64, error) {
	return backupTargetGenerations(ctx, dataset, "floatlab:removed-targets")
}

func backupTargetGenerations(ctx context.Context, dataset, property string) (map[string]int64, error) {
	raw, err := run(ctx, "zfs", "get", "-H", "-s", "local", "-o", "value", property, dataset)
	if err != nil {
		return nil, err
	}
	removed := map[string]int64{}
	if raw != "-" && raw != "" {
		if err = json.Unmarshal([]byte(raw), &removed); err != nil {
			return nil, fmt.Errorf("invalid backup removal fence: %w", err)
		}
	}
	if removed == nil {
		return nil, errors.New("invalid null backup removal fence")
	}
	for node, generation := range removed {
		if !backupID.MatchString(node) || generation < 0 {
			return nil, errors.New("invalid backup removal fence entry")
		}
	}
	return removed, nil
}

// acceptBackupGenerations requires the source dataset mutation lock.
func acceptBackupGenerations(ctx context.Context, dataset string, generations map[string]int64) error {
	accepted, err := backupTargetGenerations(ctx, dataset, TargetGenerationsProperty)
	if err != nil {
		return err
	}
	changed := false
	for node, generation := range generations {
		if !backupID.MatchString(node) || generation < 0 {
			return errors.New("invalid backup target generation")
		}
		if old, found := accepted[node]; !found || generation > old {
			accepted[node] = generation
			changed = true
		}
	}
	if changed {
		encoded, _ := json.Marshal(accepted)
		_, err = run(ctx, "zfs", "set", TargetGenerationsProperty+"="+string(encoded), dataset)
	}
	return err
}

// LockSnapshotMutation shares the durable cleanup/replication fence with snapshot creation.
func LockSnapshotMutation(dataset string) (func(), error) {
	if !validBackupDataset(dataset) {
		return nil, errors.New("invalid snapshot dataset")
	}
	return lockBackupDataset(dataset)
}
func FilterSnapshotTargets(ctx context.Context, dataset string, properties map[string]string) error {
	raw := properties[TransferTargetsProperty]
	if raw == "" {
		return nil
	}
	var nodes []string
	if err := json.Unmarshal([]byte(raw), &nodes); err != nil {
		return err
	}
	generations := map[string]int64{}
	if raw := properties["floatlab:transfer-generations"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &generations); err != nil {
			return err
		}
	}
	removed, err := removedBackupTargets(ctx, dataset)
	if err != nil {
		return err
	}
	allowed := []string{}
	accepted := map[string]int64{}
	for _, node := range nodes {
		if cutoff, found := removed[node]; !found || generations[node] > cutoff {
			allowed = append(allowed, node)
			accepted[node] = generations[node]
		} else {
			delete(generations, node)
		}
	}
	if err = acceptBackupGenerations(ctx, dataset, accepted); err != nil {
		return err
	}
	encoded, _ := json.Marshal(allowed)
	properties[TransferTargetsProperty] = string(encoded)
	encoded, _ = json.Marshal(generations)
	properties["floatlab:transfer-generations"] = string(encoded)
	return nil
}
