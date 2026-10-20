package managementupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/release"
)

type fakeBackend struct {
	calls []string
	fail  string
}

func (f *fakeBackend) call(s string) error {
	f.calls = append(f.calls, s)
	if f.fail == s {
		return errors.New(s + " failed")
	}
	return nil
}
func (f *fakeBackend) Prepare(context.Context, *Status) error   { return f.call("prepare") }
func (f *fakeBackend) Preflight(context.Context, *Status) error { return f.call("preflight") }
func (f *fakeBackend) Fence(context.Context, *Status) error     { return f.call("fence") }
func (f *fakeBackend) Snapshot(context.Context, *Status) error  { return f.call("snapshot") }
func (f *fakeBackend) Install(context.Context, *Status) error   { return f.call("install") }
func (f *fakeBackend) Healthy(context.Context, *Status) error   { return f.call("healthy") }
func (f *fakeBackend) Restore(context.Context, *Status) error   { return f.call("restore") }
func (f *fakeBackend) Authorize(context.Context, *Status) error { return f.call("authorize") }
func (f *fakeBackend) Cleanup(context.Context, *Status) error   { return f.call("cleanup") }
func TestPreflightFailureDoesNotFenceProduction(t *testing.T) {
	b := &fakeBackend{fail: "preflight"}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "running"}
	m.execute(context.Background(), s)
	if s.State != "failed" || !reflect.DeepEqual(b.calls, []string{"prepare", "preflight", "cleanup"}) {
		t.Fatalf("state %s calls %v", s.State, b.calls)
	}
}
func TestFailedUpgradeRestoresBeforeAuthorizing(t *testing.T) {
	b := &fakeBackend{fail: "install"}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "running"}
	m.execute(context.Background(), s)
	want := []string{"prepare", "preflight", "cleanup", "fence", "snapshot", "install", "restore", "healthy", "authorize"}
	if s.State != "rolled_back" || s.Error == "" || !reflect.DeepEqual(b.calls, want) {
		t.Fatalf("status %+v calls %v", s, b.calls)
	}
}
func TestInterruptedRestoreIsRetriedBeforeStartup(t *testing.T) {
	b := &fakeBackend{fail: "restore"}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "recovering", Phase: "restoring", Snapshot: "floatlab-update-test"}
	if e := m.save(s); e != nil {
		t.Fatal(e)
	}
	if e := m.Recover(context.Background()); e == nil {
		t.Fatal("expected failed restore")
	}
	got, _ := m.Status("")
	if got.State != "recovery_failed" || got.Authorized {
		t.Fatalf("unsafe status %+v", got)
	}
	b.fail = ""
	if e := m.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(b.calls, []string{"restore", "restore", "healthy", "authorize"}) {
		t.Fatal(b.calls)
	}
}
func TestInterruptedAuthorizationDoesNotRollbackCommittedData(t *testing.T) {
	b := &fakeBackend{}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "running", Phase: "authorizing", Authorized: true}
	if e := m.save(s); e != nil {
		t.Fatal(e)
	}
	if e := m.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(b.calls, []string{"authorize"}) {
		t.Fatal(b.calls)
	}
}
func TestSharedHostTransactionBlocksManagement(t *testing.T) {
	m := &Manager{Dir: t.TempDir(), Backend: &fakeBackend{}}
	if e := update.WriteJSON(filepath.Join(m.Dir, "host.json"), map[string]string{"state": "staged"}); e != nil {
		t.Fatal(e)
	}
	if _, e := m.Start(context.Background(), Request{OperationID: "test", Actor: "admin", Release: "manage-v1.0.0"}); e == nil {
		t.Fatal("accepted concurrent OS trial")
	}
}
func TestReleaseTrustBoundary(t *testing.T) {
	m := Manifest{Version: "1.0.0", Architecture: "amd64", RequiredHost: release.HostRange{Min: "0.1.0-dev", Max: "100.0.0"}, RQLiteSources: []string{"8.26.0"}, RQLiteVersion: "8.26.0", Images: map[string]string{}}
	for _, name := range []string{"floatlab-control", "rqlite", "victoria-metrics", "victoria-logs"} {
		m.Images[name] = "example/image@sha256:" + strings.Repeat("a", 64)
	}
	if e := m.Validate("manage-v1.0.0"); e != nil {
		t.Fatal(e)
	}
	m.Images["rqlite"] = "example/image:latest"
	if e := m.Validate("manage-v1.0.0"); e == nil {
		t.Fatal("accepted mutable image tag")
	}
	if validRelease("../../etc") || validRelease("manage-v1.2.3;reboot") {
		t.Fatal("accepted invalid release")
	}
}
func TestInvalidRequestsDoNotWriteJournal(t *testing.T) {
	m := &Manager{Dir: t.TempDir(), Backend: &fakeBackend{}}
	if _, e := m.Start(context.Background(), Request{OperationID: "../escape", Actor: "admin", Release: "manage-v1.0.0"}); e == nil {
		t.Fatal("accepted path traversal")
	}
	if _, e := os.Stat(m.path()); !os.IsNotExist(e) {
		t.Fatal("invalid request wrote journal")
	}
}

func TestBaselineRequiresRecoveryProtocol(t *testing.T) {
	for _, input := range []string{`{"version":"0.1.1"}`, `{"version":"0.1.1","update_protocol":2}`, `garbage`} {
		if e := validateBaseline([]byte(input)); e == nil {
			t.Fatalf("accepted unsupported baseline %s", input)
		}
	}
	if e := validateBaseline([]byte(`{"version":"0.1.2","update_protocol":1}`)); e != nil {
		t.Fatal(e)
	}
}

func TestCleanupPreservesLatestRollbackPoint(t *testing.T) {
	b := &fakeBackend{}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "succeeded", Snapshot: "floatlab-update-test", Authorized: true}
	if e := m.save(s); e != nil {
		t.Fatal(e)
	}
	got, e := m.Cleanup(context.Background(), "test")
	if e != nil {
		t.Fatal(e)
	}
	if got.Snapshot != "floatlab-update-test" || !got.Authorized {
		t.Fatalf("cleanup discarded rollback %+v", got)
	}
}

func TestAcceptedManualRollbackRevokesDurableAuthorization(t *testing.T) {
	b := &fakeBackend{}
	m := &Manager{Dir: t.TempDir(), Backend: b}
	s := &Status{OperationID: "test", State: "succeeded", Snapshot: "floatlab-update-test", Authorized: true, ProductionChanged: true}
	if e := m.save(s); e != nil {
		t.Fatal(e)
	}
	accepted, e := m.RequestRollback(context.Background(), Request{OperationID: "test", Actor: "admin", IdempotencyKey: "rollback-request"})
	if e != nil {
		t.Fatal(e)
	}
	if accepted.Authorized || accepted.Phase != "restoring" {
		t.Fatalf("accepted rollback still authorizes candidate %+v", accepted)
	}
	deadline := time.Now().Add(time.Second)
	for {
		state, e := m.Status("test")
		if e != nil {
			t.Fatal(e)
		}
		if state.State == "rolled_back" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("rollback did not finish")
		}
		time.Sleep(time.Millisecond)
	}
}
