package control

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/worker"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/floatlab/floatlab-core/pkg/run"
	"go.uber.org/zap"
)

func TestWorkloadUpgradeWindowUsesLocalCalendarAndDeduplicatesDST(t *testing.T) {
	zone, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Fatal(err)
	}
	window, err := ParseWorkloadUpgradeWindow(DefaultWorkloadUpgradeWindow)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ utc, slot string }{
		{"2026-04-04T15:29:00Z", ""},
		{"2026-04-04T15:30:00Z", "2026-04-05"},
		{"2026-04-04T16:30:00Z", "2026-04-05"},
		{"2026-04-04T17:30:00Z", ""},
		{"2026-10-03T16:00:00Z", "2026-10-04"},
	} {
		instant, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		if got := window.slot(instant.In(zone)); got != tc.slot {
			t.Fatalf("%s slot=%q want=%q", instant.In(zone), got, tc.slot)
		}
	}
	if _, err := ParseWorkloadUpgradeWindow("02:30-02:30"); err == nil {
		t.Fatal("accepted zero-length upgrade window")
	}
}

func TestWorkloadUpgradeAdmissionSkipsFailedBatchAndAlertsOnDisabledUpdate(t *testing.T) {
	ctx := t.Context()
	s, db := peerWorkloadServer(t, "node-a")
	digest := func(character string) string { return "sha256:" + strings.Repeat(character, 64) }
	web, database := "caddy:2.13.3@"+digest("a"), "postgres:17.4@"+digest("b")
	stack := &config.Stack{ID: "apps", Name: "apps", PrimaryNodeID: "node-a", ZFSDataset: "floatlab/apps", ComposeYAML: "services:\n  web:\n    image: " + web + "\n    x-fl-managed-image: " + web + "\n  database:\n    image: " + database + "\n    x-fl-auto-upgrade: disabled\n"}
	if err := s.store.CreateStack(ctx, stack); err != nil {
		t.Fatal(err)
	}
	if err := s.raft.Apply(run.StackStateChanged{StackID: stack.ID, To: run.StateRunningPrimary, Timestamp: time.Now().UTC(), NodeID: stack.PrimaryNodeID}, time.Second); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "fl-image-upgrades-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "host.sock")
	daemon := ipc.NewServer(socket, zap.NewNop())
	daemon.Handle("compose.running-images", func(context.Context, json.RawMessage) (any, error) {
		return ipc.ComposeRunningImages{Matches: true, Images: map[string]ipc.RunningImage{
			"web": {Reference: web, Digests: []string{digest("a")}}, "database": {Reference: database, Digests: []string{digest("b")}},
		}}, nil
	})
	var registryImage atomic.Value
	firstCandidate := "caddy:2.13.4@" + digest("c")
	registryImage.Store(firstCandidate)
	daemon.Handle("docker.image.lookup", func(_ context.Context, raw json.RawMessage) (any, error) {
		var request ipc.ImageLookupPayload
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		if request.Policy == "" {
			return ipc.ImageLookupResult{Image: web}, nil
		}
		if request.Policy == "disabled" {
			return ipc.ImageLookupResult{Image: "postgres:18.2@" + digest("d")}, nil
		}
		return ipc.ImageLookupResult{Image: registryImage.Load().(string)}, nil
	})
	go func() { _ = daemon.Run(ctx) }()
	waitForSocket(t, socket)
	s.hosts = hostclient.NewPool(zap.NewNop())
	t.Cleanup(s.hosts.Close)
	s.hosts.Register(stack.PrimaryNodeID, socket)
	key := imageBatchKey(map[string]string{"web": firstCandidate, "formerly-eligible": "alpine:3.22.1"})
	if err := db.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO failed_workload_upgrades VALUES(?,?)`, Params: []any{stack.ID, key}}}); err != nil {
		t.Fatal(err)
	}
	check := func(slot string) {
		t.Helper()
		if err := s.checkStackImageUpgrades(ctx, stack, slot, time.Now().Add(time.Minute), true); err != nil {
			t.Fatal(err)
		}
	}
	check("2026-11-24")
	check("2026-11-25")
	if active, err := s.ops.Active(ctx); err != nil || len(active) != 0 {
		t.Fatalf("failed candidate was retried: %v, %v", active, err)
	}
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT message FROM alerts WHERE stack_id=? AND kind='image-upgrade-available' AND state='active'`, Params: []any{stack.ID}})
	if err != nil || len(result.Values) != 1 || !strings.Contains(result.Values[0][0].(string), "postgres:18.2") {
		t.Fatalf("disabled service alert: %+v, %v", result, err)
	}
	newer := "caddy:2.13.5@" + digest("e")
	registryImage.Store(newer)
	check("2026-11-26")
	rows, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT payload FROM tasks WHERE stack_id=? AND type=?`, Params: []any{stack.ID, worker.TaskStackUpgrade}})
	if err != nil || len(rows.Values) != 1 {
		t.Fatalf("planned upgrade: %+v, %v", rows, err)
	}
	var payload worker.StackUpgradePayload
	if err := json.Unmarshal([]byte(rows.Values[0][0].(string)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Services) != 1 || payload.Services[0] != "web" || !strings.Contains(payload.NewCompose, newer) || !strings.Contains(payload.NewCompose, database) || !strings.Contains(payload.RollbackCompose, web) {
		t.Fatalf("unexpected upgrade: %+v", payload)
	}

	source := "x-fl-stack:\n  primary_node: node-a\n  storage: {pool: floatlab}\nservices:\n  web:\n    image: caddy:latest\n    x-fl-auto-upgrade: disabled\n"
	request, err := json.Marshal(createStackRequest{Name: "new-workload", ComposeFile: source})
	if err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, s, http.MethodPost, "/api/v1/stacks", string(request))
	var saved stackResponse
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &saved) != nil || !strings.Contains(saved.ComposeFile, "image: "+web) || !strings.Contains(saved.ComposeFile, "x-fl-auto-upgrade: disabled") {
		t.Fatalf("save-time pin: %d %s", response.Code, response.Body.String())
	}
}

func TestSaveComposeRejectsStaleSource(t *testing.T) {
	s, _ := peerWorkloadServer(t, "node-a")
	stack := &config.Stack{ID: "app", Name: "app", PrimaryNodeID: "node-a", ComposeYAML: "services: {}\n"}
	if err := s.store.CreateStack(t.Context(), stack); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, s, http.MethodPut, "/api/v1/stacks/app/compose", `{"compose_file":"services: {}","expected_compose_file":"outdated"}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "compose_changed") {
		t.Fatalf("save response: %d %s", response.Code, response.Body.String())
	}
}

func TestWorkloadUpgradeSettings(t *testing.T) {
	s, _ := peerWorkloadServer(t, "node-a")
	const path = "/api/v1/settings/workload-upgrades"
	read := func(want string) {
		t.Helper()
		response := peerRequest(t, s, http.MethodGet, path, "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Fatalf("get upgrade settings: %d %s", response.Code, response.Body.String())
		}
	}
	read(DefaultWorkloadUpgradeWindow)
	if response := peerRequest(t, s, http.MethodPut, path, `{"window":"03:30-03:30"}`); response.Code != http.StatusBadRequest {
		t.Fatalf("accepted an invalid upgrade window: %d %s", response.Code, response.Body.String())
	}
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(`{"window":"03:00-04:00"}`))
	request.Header.Set("Authorization", "Bearer "+signedToken(t, "test-secret", []string{"admin"}))
	request.Header.Set("Idempotency-Key", "valid-window")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("set upgrade settings: %d %s", response.Code, response.Body.String())
	}
	read("03:00-04:00")
}
