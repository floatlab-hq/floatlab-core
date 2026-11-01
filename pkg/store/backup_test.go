package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type memoryBackup struct {
	source               []BackupSnapshot
	state                BackupState
	sends                [][]string
	held                 map[string]bool
	fail                 bool
	failWithoutToken     bool
	failSendNumber       int
	protectError         error
	unavailableAfterSend bool
	tokenError           error
	stateError           error
	aborted              bool
	pruned               bool
	pruneError           error
	dropSnapshot         string
	events               []string
}

func (m *memoryBackup) Source(context.Context, string) ([]BackupSnapshot, error) {
	return m.source, nil
}
func (m *memoryBackup) State(context.Context) (BackupState, error) {
	if m.unavailableAfterSend && len(m.sends) > 0 {
		return BackupState{}, errors.New("destination state unknown")
	}
	if m.stateError != nil {
		return BackupState{}, m.stateError
	}
	if len(m.sends) > 0 && m.dropSnapshot != "" {
		filtered := []BackupSnapshot{}
		for _, s := range m.state.Snapshots {
			if s.Name != m.dropSnapshot {
				filtered = append(filtered, s)
			}
		}
		m.state.Snapshots = filtered
	}
	return m.state, nil
}
func (m *memoryBackup) Send(_ context.Context, args []string) (int64, error) {
	m.sends = append(m.sends, args)
	if m.failWithoutToken || m.failSendNumber == len(m.sends) {
		return 0, errors.New("send failed before receive stream")
	}
	if m.fail {
		m.state.ResumeToken = "saved-token"
		return 0, errors.New("connection interrupted")
	}
	if len(args) > 1 && args[1] == "-t" {
		m.state.ResumeToken = ""
		m.state.Snapshots = append([]BackupSnapshot{}, m.source...)
		return 42, nil
	}
	target := strings.Split(args[len(args)-1], "@")[1]
	start := 0
	if len(args) > 2 && args[1] == "-I" {
		base := strings.Split(args[2], "@")[1]
		for i, s := range m.source {
			if s.Name == base {
				start = i + 1
			}
		}
	}
	for _, s := range m.source[start:] {
		m.state.Snapshots = append(m.state.Snapshots, s)
		if s.Name == target {
			break
		}
	}
	return 42, nil
}
func (m *memoryBackup) Abort(context.Context) error {
	m.aborted = true
	m.state.ResumeToken = ""
	return nil
}
func (m *memoryBackup) ValidateToken(context.Context, string) error { return m.tokenError }
func (m *memoryBackup) Protect(_ context.Context, snapshot, node, kind string, add bool) error {
	if m.protectError != nil && kind == "hold" && add {
		return m.protectError
	}
	if m.held == nil {
		m.held = map[string]bool{}
	}
	key := snapshot + ":" + kind + ":" + node
	m.held[key] = add
	m.events = append(m.events, key)
	return nil
}
func (m *memoryBackup) Prune(context.Context) error { m.pruned = true; return m.pruneError }
func request() ReplicationRequest {
	return ReplicationRequest{Dataset: "tank/workload", Snapshot: "fl-c", SourceNodeID: "source", DestNodeID: "backup", JobID: "job"}
}
func snapshots() []BackupSnapshot {
	return []BackupSnapshot{{"fl-a", "1"}, {"fl-b", "2"}, {"fl-c", "3"}}
}
func TestReplicationSeedsAllIntermediateSnapshots(t *testing.T) {
	m := &memoryBackup{source: snapshots()}
	r := request()
	if _, err := ReplicateBackup(context.Background(), r, m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.state.Snapshots, m.source) {
		t.Fatalf("intermediate snapshots missing: %+v", m.state)
	}
	if len(m.sends) != 2 || m.sends[1][1] != "-I" {
		t.Fatalf("not inclusive incremental: %v", m.sends)
	}
	if !m.held["fl-c:hold:backup"] || m.held["fl-a:hold:backup"] || m.held["fl-c:transfer:backup"] || !m.pruned {
		t.Fatalf("protection not advanced: %v", m.held)
	}
}
func TestReplicationResumesAbortedTransfer(t *testing.T) {
	m := &memoryBackup{source: snapshots(), fail: true}
	r := request()
	if _, err := ReplicateBackup(context.Background(), r, m); err == nil {
		t.Fatal("expected interruption")
	}
	if !m.held["fl-a:transfer:backup"] || m.pruned {
		t.Fatal("failed stream lost protection or pruned")
	}
	m.fail = false
	if _, err := ReplicateBackup(context.Background(), r, m); err != nil {
		t.Fatal(err)
	}
	if m.sends[1][1] != "-t" || m.state.ResumeToken != "" || !m.pruned {
		t.Fatalf("did not resume saved stream: %v", m.sends)
	}
}
func TestReplicationRetiresInvalidToken(t *testing.T) {
	m := &memoryBackup{source: snapshots(), state: BackupState{ResumeToken: "bad"}, tokenError: ErrInvalidResumeToken}
	if _, err := ReplicateBackup(context.Background(), request(), m); err != nil {
		t.Fatal(err)
	}
	if !m.aborted || len(m.state.Snapshots) != 3 {
		t.Fatal("invalid partial receive not retired and reseeded")
	}
}
func TestReplicationDoesNotAbortOnTemporaryValidationFailure(t *testing.T) {
	m := &memoryBackup{source: snapshots(), state: BackupState{ResumeToken: "token"}, tokenError: errors.New("source unavailable")}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected failure")
	}
	if m.aborted || m.pruned {
		t.Fatal("temporary failure retired destination state")
	}
}
func TestReplicationPreservesUnrelatedDestinationAndSourceHolds(t *testing.T) {
	m := &memoryBackup{source: snapshots(), state: BackupState{Snapshots: []BackupSnapshot{{"fl-older", "older"}, {"fl-a", "1"}}}, held: map[string]bool{"fl-a:hold:other": true, "fl-a:hold:backup": true}}
	if _, err := ReplicateBackup(context.Background(), request(), m); err != nil {
		t.Fatal(err)
	}
	if !m.held["fl-a:hold:other"] || m.held["fl-a:hold:backup"] {
		t.Fatal("removed unrelated node protection")
	}
	if m.state.Snapshots[0].GUID != "older" {
		t.Fatal("older backup history lost")
	}
}
func TestReplicationRejectsMissingCommonBase(t *testing.T) {
	m := &memoryBackup{source: snapshots(), state: BackupState{Snapshots: []BackupSnapshot{{"fl-other", "other"}}}}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected missing common base failure")
	}
	if len(m.sends) > 0 || m.aborted || m.pruned {
		t.Fatal("missing common base modified backup")
	}
}
func TestBackupReceiverRejectsForeignDestinations(t *testing.T) {
	for _, command := range []string{"zfs-backup state tank/workload -", "zfs-backup state tank/backups/other/tank/data -", "zfs-backup receive tank/backups/source/tank/data;touch -", "zfs-backup shell tank/backups/source/tank/data -"} {
		if err := ServeBackupReceiver(context.Background(), "source", command, nil, nil, nil); err == nil {
			t.Fatalf("accepted %q", command)
		}
	}
}
func TestSSHRequiresPinnedLabIdentity(t *testing.T) {
	b := commandBackup{ReplicationRequest{DestNodeID: "peer", DestHost: "::1", DestPort: 22, DestDataset: "tank/backups/source/tank/data", IdentityFile: "key", KnownHostsFile: "known"}}
	args := strings.Join(b.remoteArgs("state"), " ")
	for _, want := range []string{"StrictHostKeyChecking=yes", "IdentitiesOnly=yes", "HostKeyAlias=peer", "UserKnownHostsFile=known", "-i key", "floatlab-replica@::1", "zfs-backup state"} {
		if !strings.Contains(args, want) {
			t.Fatalf("missing %q in %s", want, args)
		}
	}
}
func TestReplicationStaleTaskDoesNotRegressRecoveryPoint(t *testing.T) {
	m := &memoryBackup{source: snapshots(), state: BackupState{Snapshots: snapshots()}, held: map[string]bool{"fl-c:hold:backup": true}}
	r := request()
	r.Snapshot = "fl-a"
	if _, err := ReplicateBackup(context.Background(), r, m); err != nil {
		t.Fatal(err)
	}
	if len(m.sends) > 0 || !m.held["fl-c:hold:backup"] || m.held["fl-a:hold:backup"] || m.pruned {
		t.Fatalf("stale task regressed recovery point: %v", m.held)
	}
}
func TestReplicationVerifiesIntermediateGUIDs(t *testing.T) {
	m := &memoryBackup{source: snapshots(), dropSnapshot: "fl-b"}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("accepted missing intermediate snapshot")
	}
	if m.pruned || m.held["fl-c:hold:backup"] {
		t.Fatal("verification failure advanced protection or pruned")
	}
}
func TestReplicationReportsPruneFailureSeparately(t *testing.T) {
	m := &memoryBackup{source: snapshots(), pruneError: errors.New("destroy failed")}
	result, err := ReplicateBackup(context.Background(), request(), m)
	if err != nil || result.PruneError == "" || !m.held["fl-c:hold:backup"] {
		t.Fatalf("verified replication lost to prune failure: result=%+v err=%v", result, err)
	}
}
func TestCleanupCancellationTargetsOnlyRemovedNode(t *testing.T) {
	removed, done := registerBackup(context.Background(), "tank/data@removed", 2)
	defer done()
	other, finish := registerBackup(context.Background(), "tank/data@other", 2)
	defer finish()
	cancelBackups("tank/data@removed", 2)
	if removed.Err() == nil || other.Err() != nil {
		t.Fatal("cleanup cancelled wrong active transfer")
	}
}
func TestReplicationOfflinePreflightPreservesOnlyLastVerifiedHold(t *testing.T) {
	m := &memoryBackup{source: snapshots(), stateError: errors.New("destination offline"), held: map[string]bool{"fl-a:hold:backup": true, "fl-a:hold:other": true}}
	for _, name := range []string{"fl-b", "fl-c"} {
		r := request()
		r.Snapshot = name
		if _, err := ReplicateBackup(context.Background(), r, m); err == nil {
			t.Fatal("offline replication succeeded")
		}
	}
	want := map[string]bool{"fl-a:hold:backup": true, "fl-a:hold:other": true}
	if !reflect.DeepEqual(m.held, want) || len(m.events) != 0 || len(m.sends) != 0 || m.pruned {
		t.Fatalf("offline preflight reserved unsent history: holds=%v events=%v", m.held, m.events)
	}
}
func TestReplicationFailureWithoutPartialReceiveReleasesTransferHolds(t *testing.T) {
	m := &memoryBackup{source: snapshots(), failWithoutToken: true, held: map[string]bool{"fl-a:hold:backup": true, "fl-a:transfer:other": true}}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected send failure")
	}
	for key, held := range m.held {
		if held && strings.HasSuffix(key, ":transfer:backup") {
			t.Fatalf("non-resumable transfer left source history pinned: %s", key)
		}
	}
	if !m.held["fl-a:hold:backup"] || !m.held["fl-a:transfer:other"] || m.pruned {
		t.Fatal("failed receive disturbed recovery point or another node")
	}
}
func TestReplicationFailureWithUnknownReceiveStatePreservesTransferHolds(t *testing.T) {
	m := &memoryBackup{source: snapshots(), failWithoutToken: true, unavailableAfterSend: true}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected send failure")
	}
	for _, name := range []string{"fl-a", "fl-b", "fl-c"} {
		if !m.held[name+":transfer:backup"] {
			t.Fatalf("unknown receive state lost resume protection for %s", name)
		}
	}
}

func TestReplicationFailedCatchupPinsCompletedSeedBeforePruning(t *testing.T) {
	m := &memoryBackup{source: snapshots(), failSendNumber: 2}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected incremental send failure")
	}
	if !m.held["fl-a:hold:backup"] || m.state.ResumeToken != "" || m.pruned {
		t.Fatalf("completed seed lost its recovery point: holds=%v state=%+v", m.held, m.state)
	}
	// Model source retention expiring all unheld snapshots, then retry a new point.
	kept := []BackupSnapshot{}
	for _, s := range m.source {
		if m.held[s.Name+":hold:backup"] || m.held[s.Name+":transfer:backup"] {
			kept = append(kept, s)
		}
	}
	if len(kept) != 1 || kept[0].Name != "fl-a" {
		t.Fatalf("non-resumable failure retained more than its recovery point: %v", kept)
	}
	m.source = append(kept, BackupSnapshot{"fl-d", "4"})
	r := request()
	r.Snapshot = "fl-d"
	if _, err := ReplicateBackup(context.Background(), r, m); err != nil {
		t.Fatal(err)
	}
	if !m.held["fl-d:hold:backup"] || m.held["fl-a:hold:backup"] {
		t.Fatalf("retry did not advance recovery protection: %v", m.held)
	}
}

func TestReplicationFailedCatchupPreservesTransfersIfRecoveryHoldFails(t *testing.T) {
	m := &memoryBackup{source: snapshots(), failSendNumber: 2, protectError: errors.New("hold failed")}
	if _, err := ReplicateBackup(context.Background(), request(), m); err == nil {
		t.Fatal("expected send and recovery hold failure")
	}
	for _, s := range m.source {
		if !m.held[s.Name+":transfer:backup"] {
			t.Fatalf("failed recovery reconciliation released %s", s.Name)
		}
	}
}

func TestDelayedCleanupPreservesNewerGeneration(t *testing.T) {
	dir := t.TempDir()
	for name, value := range map[string]string{"floatlab:removed-targets": "{}", TargetGenerationsProperty: "{}"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Unexpected list/hold/release/destroy calls fail: stale cleanup must stop before any.
	script := `#!/bin/sh
case "$1" in
get) cat "$BACKUP_TEST_STATE/$7" ;;
set) name=${2%%=*}; value=${2#*=}; printf '%s' "$value" > "$BACKUP_TEST_STATE/$name" ;;
*) echo "unexpected zfs action: $*" >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "zfs"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BACKUP_TEST_STATE", dir)
	ctx := context.Background()
	properties := map[string]string{TransferTargetsProperty: `["backup"]`, "floatlab:transfer-generations": `{"backup":3}`}
	if err := FilterSnapshotTargets(ctx, "tank/data", properties); err != nil {
		t.Fatal(err)
	}
	active, finish := registerBackup(ctx, "tank/data@backup", 3)
	defer finish()
	cancelBackups("tank/data@backup", 2)
	if active.Err() != nil {
		t.Fatal("stale cleanup cancelled newly added target")
	}
	if err := cleanupBackupGenerationLocked(ctx, "tank/data", "backup", "h/24", 2); err != nil {
		t.Fatal(err)
	}
	accepted, err := backupTargetGenerations(ctx, "tank/data", TargetGenerationsProperty)
	if err != nil || accepted["backup"] != 3 {
		t.Fatalf("lost newer source generation: %v %v", accepted, err)
	}
	// A duplicate old removal cannot lower a newer removal fence either.
	if _, err := run(ctx, "zfs", "set", `floatlab:removed-targets={"backup":4}`, "tank/data"); err != nil {
		t.Fatal(err)
	}
	if err := cleanupBackupGenerationLocked(ctx, "tank/data", "backup", "h/24", 2); err != nil {
		t.Fatal(err)
	}
	removed, err := removedBackupTargets(ctx, "tank/data")
	if err != nil || removed["backup"] != 4 {
		t.Fatalf("removal fence regressed: %v %v", removed, err)
	}
	if err := acceptBackupGenerations(ctx, "tank/data", map[string]int64{"backup": 1}); err != nil {
		t.Fatal(err)
	}
	accepted, err = backupTargetGenerations(ctx, "tank/data", TargetGenerationsProperty)
	if err != nil || accepted["backup"] != 3 {
		t.Fatalf("accepted generation regressed: %v %v", accepted, err)
	}
}
