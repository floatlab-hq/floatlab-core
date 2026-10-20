package hostupgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/floatlab/floatlab-core/internal/update"
)

func manifest(version string) Manifest {
	return Manifest{Payload: installer.Payload{Version: version, Init: "/nix/store/test/init", RootBytes: 1, Bytes: 1, Files: map[string]string{"floatlab-root.tar.gz": strings.Repeat("a", 64), "zfsbootmenu.EFI": strings.Repeat("b", 64)}}, Architecture: "amd64", RecoveryProtocol: 1, Bootloader: "zfsbootmenu-3.1.0", PoolCompatibility: "openzfs-2.3-linux", Engine: Engine{"29.5.1", "2.2.0", "zfs", "/floatlab/system/docker"}, QualifiedSources: []string{"1.0.0"}}
}

type fakeBackend struct {
	calls                        []string
	fail                         string
	version, origin, token, boot string
	stageStarted                 chan struct{}
	stageContinue                chan struct{}
}

func (f *fakeBackend) call(name string) error {
	f.calls = append(f.calls, name)
	if name == f.fail {
		return errors.New(name + " failed")
	}
	return nil
}
func (f *fakeBackend) Stage(context.Context, Request) (Manifest, Manifest, error) {
	if f.stageStarted != nil {
		close(f.stageStarted)
		<-f.stageContinue
	}
	return manifest("2.0.0"), manifest("1.0.0"), f.call("stage")
}
func (f *fakeBackend) ValidateActivation(context.Context, Status) error { return f.call("validate") }
func (f *fakeBackend) Arm(context.Context, Status) error                { return f.call("arm") }
func (f *fakeBackend) Clear(context.Context) error                      { return f.call("clear") }
func (f *fakeBackend) Reboot(context.Context) error                     { return f.call("reboot") }
func (f *fakeBackend) Running(context.Context) (string, string, string, string, error) {
	return f.version, f.origin, f.token, f.boot, nil
}
func (f *fakeBackend) Health(context.Context) error                 { return f.call("health") }
func (f *fakeBackend) Promote(context.Context, Status) error        { return f.call("promote") }
func (f *fakeBackend) FinalizeCommit(context.Context, Status) error { return f.call("finalize") }
func (f *fakeBackend) RestoreBoot(context.Context, Status) error    { return f.call("restore-boot") }
func (f *fakeBackend) Cleanup(context.Context, Status) error        { return f.call("cleanup") }

func setup(t *testing.T, state string) (*Manager, *fakeBackend) {
	t.Helper()
	f := &fakeBackend{version: "1.0.0", origin: "floatlab/os/1.0.0@base", boot: "old-boot"}
	m := &Manager{Dir: t.TempDir(), Backend: f}
	m.Checkpoint = func(context.Context, string) error { return f.call("checkpoint") }
	m.RestoreCheckpoint = func(context.Context, string) error { return f.call("restore-management") }
	m.ReleaseCheckpoint = func(context.Context, string) error { return f.call("release") }
	m.TrialStart = func(context.Context, string) error { return f.call("trial-start") }
	m.TrialStop = func(context.Context, string) error { return f.call("trial-stop") }
	m.ValidateManagement = func(context.Context, string) error { return f.call("management-compatible") }
	s := Status{OperationID: "op", Actor: "admin", State: state, Phase: state, Source: manifest("1.0.0"), Manifest: manifest("2.0.0"), SourceRelease: "host-v1.0.0", TargetRelease: "host-v2.0.0", BootID: "old-boot"}
	if err := m.save(&s, state); err != nil {
		t.Fatal(err)
	}
	return m, f
}
func request() Request {
	return Request{OperationID: "op", Actor: "admin", Release: "host-v2.0.0", IdempotencyKey: "request-one"}
}

func TestCleanupRemovesOnlyTerminalOperationsDownloads(t *testing.T) {
	for _, state := range []string{"recovered", "failed", "committed", "staged", "armed", "trial", "ready"} {
		t.Run(state, func(t *testing.T) {
			m, _ := setup(t, state)
			paths := []string{}
			for _, id := range []string{"op", "retained-operation", "trial-operation"} {
				for _, name := range []string{"floatlab-root.tar.gz", "floatlab-root.tar.gz.partial", "host-manifest.json"} {
					path := filepath.Join(m.Dir, id, name)
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("retained fixture"), 0600); err != nil {
						t.Fatal(err)
					}
					paths = append(paths, path)
				}
			}
			_, err := m.Cleanup(context.Background(), request())
			if (err == nil) != terminal(state) {
				t.Fatalf("cleanup state %s: %v", state, err)
			}
			for _, path := range paths {
				_, err := os.Stat(path)
				removed := terminal(state) && filepath.Base(filepath.Dir(path)) == "op" && filepath.Base(path) != "host-manifest.json"
				if removed && !os.IsNotExist(err) || !removed && err != nil {
					t.Fatalf("removed=%v path=%s err=%v", removed, path, err)
				}
			}
			if terminal(state) {
				if _, err := m.Cleanup(context.Background(), request()); err != nil {
					t.Fatalf("replayed cleanup: %v", err)
				}
			}
		})
	}
}

func TestActivationCheckpointAndArmingFailuresRecoverBeforePolicyRelease(t *testing.T) {
	for _, failure := range []string{"checkpoint", "arm", "reboot"} {
		t.Run(failure, func(t *testing.T) {
			m, f := setup(t, "staged")
			f.fail = failure
			s, err := m.Activate(context.Background(), request())
			if err != nil {
				t.Fatal(err)
			}
			if s.State != "recovered" || s.Error == "" {
				t.Fatalf("%+v", s)
			}
			tail := f.calls[len(f.calls)-3:]
			if !reflect.DeepEqual(tail, []string{"clear", "restore-boot", "restore-management"}) {
				t.Fatal(f.calls)
			}
		})
	}
}
func TestUnknownCompatibilityNeverStopsProduction(t *testing.T) {
	m, f := setup(t, "staged")
	f.fail = "validate"
	_, err := m.Activate(context.Background(), request())
	if err == nil || !reflect.DeepEqual(f.calls, []string{"validate"}) {
		t.Fatalf("%v %v", f.calls, err)
	}
}
func TestFallbackRestoresBeforeStartupAndIsIdempotent(t *testing.T) {
	m, f := setup(t, "ready")
	f.boot = "new-boot"
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"clear", "restore-boot", "restore-management"}) {
		t.Fatal(f.calls)
	}
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 3 {
		t.Fatal(f.calls)
	}
}
func TestTrialMustMatchOriginAndOperation(t *testing.T) {
	for _, token := range []string{"wrong", "op"} {
		m, f := setup(t, "armed")
		f.version = "2.0.0"
		f.origin = "floatlab/os/2.0.0@base"
		f.token = token
		f.boot = "trial-boot"
		err := m.Recover(context.Background())
		if token == "wrong" {
			if err == nil || len(f.calls) != 0 {
				t.Fatalf("%v %v", f.calls, err)
			}
		} else {
			if err != nil || !reflect.DeepEqual(f.calls, []string{"trial-start"}) {
				t.Fatalf("%v %v", f.calls, err)
			}
		}
	}
}
func TestFailedRestorationKeepsRecoveryPending(t *testing.T) {
	m, f := setup(t, "trial")
	f.boot = "fallback-boot"
	f.fail = "restore-management"
	if err := m.Recover(context.Background()); err == nil {
		t.Fatal("accepted failed restoration")
	}
	s, _ := m.Status()
	if s.State != "recovering" || s.RollbackError == "" {
		t.Fatalf("%+v", s)
	}
}
func TestCommitRequiresReadinessAndInterruptedCommitFinishesOnce(t *testing.T) {
	m, f := setup(t, "trial")
	if _, err := m.Commit(context.Background(), request()); err == nil {
		t.Fatal("unready commit accepted")
	}
	s, _ := m.Status()
	_ = m.save(&s, "committing")
	f.version = "2.0.0"
	f.origin = "floatlab/os/2.0.0@base"
	f.token = "op"
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ = m.Status()
	if s.State != "committed" {
		t.Fatal(s.State)
	}
	if !reflect.DeepEqual(f.calls, []string{"promote", "release", "finalize", "cleanup"}) {
		t.Fatal(f.calls)
	}
}
func TestFinalizationFailureRetriesOnNextRecovery(t *testing.T) {
	m, f := setup(t, "ready")
	f.version = "2.0.0"
	f.origin = "floatlab/os/2.0.0@base"
	f.token = "op"
	f.fail = "release"
	if _, err := m.Commit(context.Background(), request()); err == nil {
		t.Fatal("release failure ignored")
	}
	f.fail = ""
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Status()
	if !s.CommitFinalized {
		t.Fatal("did not finalize")
	}
	before := len(f.calls)
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != before {
		t.Fatal("finalized release repeated")
	}
}
func TestPendingAndConcurrentRequestsBlocked(t *testing.T) {
	m, _ := setup(t, "ready")
	if _, err := m.Prepare(context.Background(), Request{OperationID: "second", Actor: "admin", Release: "host-v3.0.0"}); err == nil {
		t.Fatal("accepted second trial")
	}
	release, err := update.Acquire(m.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = m.Cleanup(context.Background(), request()); err == nil {
		t.Fatal("lock bypassed")
	}
}
func TestInterruptedStageFailsWithoutStoppingStack(t *testing.T) {
	m, f := setup(t, "staging")
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := m.Status()
	if s.State != "failed" || len(f.calls) != 0 {
		t.Fatalf("%+v %v", s, f.calls)
	}
}
func TestPrepareAcknowledgesBeforeDownloadAndReservesLane(t *testing.T) {
	m, f := setup(t, "failed")
	f.stageStarted = make(chan struct{})
	f.stageContinue = make(chan struct{})
	r := Request{OperationID: "new", Actor: "admin", Release: "host-v2.0.0"}
	s, err := m.Prepare(context.Background(), r)
	if err != nil || s.State != "staging" {
		t.Fatalf("%+v %v", s, err)
	}
	<-f.stageStarted
	if _, err = update.Acquire(m.Dir); err == nil {
		t.Fatal("staging did not reserve shared lane")
	}
	close(f.stageContinue)
	for i := 0; i < 100; i++ {
		s, _ = m.Status()
		if s.State == "staged" {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal(s.State)
}
func TestMalformedDurableJournalFailsClosed(t *testing.T) {
	m, _ := setup(t, "trial")
	if err := os.WriteFile(filepath.Join(m.Dir, "host.json"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err == nil {
		t.Fatal("corrupt journal accepted")
	}
}

func TestAcceptedRollbackOnTargetResumesRebootWithoutRestoringDatabaseThere(t *testing.T) {
	for _, failure := range []string{"trial-stop", "clear", "restore-boot", "reboot"} {
		t.Run(failure, func(t *testing.T) {
			m, f := setup(t, "recovering")
			f.version = "2.0.0"
			f.origin = "floatlab/os/2.0.0@base"
			f.boot = "target-boot"
			f.fail = failure
			if err := m.Recover(context.Background()); err == nil {
				t.Fatal("failure ignored")
			}
			s, _ := m.Status()
			if s.State != "recovering" {
				t.Fatal(s.State)
			}
			f.calls = nil
			f.fail = ""
			if err := m.Recover(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(f.calls, []string{"trial-stop", "clear", "restore-boot", "reboot"}) {
				t.Fatal(f.calls)
			}
		})
	}
}

func TestStatusForNeverReturnsAnotherOperation(t *testing.T) {
	m, _ := setup(t, "staged")
	if _, err := m.StatusFor("other"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if s, err := m.StatusFor("op"); err != nil || s.OperationID != "op" {
		t.Fatal(s, err)
	}
}

func TestArbitraryBoundedIdempotencyKeysAreHashedBeforeFilesystemUse(t *testing.T) {
	m, _ := setup(t, "failed")
	r := request()
	r.IdempotencyKey = "request/../../with spaces:✓"
	s, err := m.Submit(context.Background(), r, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		release, e := update.Acquire(m.Dir)
		if e == nil {
			release()
			break
		}
		time.Sleep(time.Millisecond)
	}
	entries, err := os.ReadDir(filepath.Join(m.Dir, "host-actions"))
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	if strings.ContainsAny(entries[0].Name(), " /:") {
		t.Fatal(entries[0].Name())
	}
	if s.OperationID != "op" {
		t.Fatal(s)
	}
}

func TestTransitionRequiresExactQualifiedSourceAndUndriftedRuntime(t *testing.T) {
	source, target := manifest("1.0.0"), manifest("2.0.0")
	if err := ValidateTransition(source, target, source.Engine); err != nil {
		t.Fatal(err)
	}
	target.QualifiedSources = nil
	if err := ValidateTransition(source, target, source.Engine); err == nil {
		t.Fatal("unqualified source accepted")
	}
	target.QualifiedSources = []string{"1.0.0"}
	engine := source.Engine
	engine.Containerd = "other"
	if err := ValidateTransition(source, target, engine); err == nil {
		t.Fatal("runtime drift accepted")
	}
	target.Bootloader = "new"
	if err := ValidateTransition(source, target, source.Engine); err == nil {
		t.Fatal("bootloader change accepted")
	}
	source.RecoveryProtocol = 0
	if err := ValidateTransition(source, manifest("2.0.0"), source.Engine); err == nil {
		t.Fatal("old baseline accepted")
	}
}
func TestTrialTokenRejectsDuplicatesAndTraversal(t *testing.T) {
	for _, line := range []string{"floatlab.trial=../../etc", "floatlab.trial=", "floatlab.trial=op floatlab.trial=other"} {
		if _, err := TrialToken(line); err == nil {
			t.Fatal(line)
		}
	}
	if token, err := TrialToken("root=zfs:floatlab/os/next floatlab.trial=op-123"); err != nil || token != "op-123" {
		t.Fatal(token, err)
	}
}
func TestGuardStartupAndHeartbeatDeadlinesCannotBeDisarmedByOtherOperation(t *testing.T) {
	start := time.Now()
	g := GuardState{OperationID: "op", Started: start}
	g.Observe(start.Add(9*time.Minute), "wrong", "ready", 1)
	if !g.Expired(start.Add(10 * time.Minute)) {
		t.Fatal("wrong operation disarmed deadline")
	}
	g.Observe(start.Add(30*time.Second), "op", "ready", 1)
	if g.Expired(start.Add(80 * time.Second)) {
		t.Fatal("healthy heartbeat expired")
	}
	g.Observe(start.Add(89*time.Second), "op", "ready", 1)
	if !g.Expired(start.Add(90 * time.Second)) {
		t.Fatal("replayed heartbeat renewed deadline")
	}
	g.Observe(start.Add(91*time.Second), "op", "ready", 2)
	if g.Expired(start.Add(140 * time.Second)) {
		t.Fatal("new heartbeat rejected")
	}
}
func TestEFIBootEntryMustPointToDedicatedTrialExecutable(t *testing.T) {
	if bootEntry.FindStringSubmatch(`Boot0001* FloatLab Trial HD(1,GPT,4da5108c-27fc-43ad-8e2b-a1fd1a42bebe,0,1)/File(\EFI\FloatLab\TRIAL.EFI)`) == nil {
		t.Fatal("trial rejected")
	}
	if bootEntry.FindStringSubmatch(`Boot0001* FloatLab Trial HD(1,GPT,4da5108c-27fc-43ad-8e2b-a1fd1a42bebe,0,1)/File(\EFI\BOOT\BOOTX64.EFI)`) != nil {
		t.Fatal("fallback executable selected")
	}
}
