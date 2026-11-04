package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// This test uses the appliance's real pinned sidecar: the fast Python SQL fixture
// cannot check rqlite's HTTP validation, transaction errors or response metadata.
func runTestPinnedRQLiteMigrationAndLocalAuthority(t ginkgo.GinkgoTInterface) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	name := fmt.Sprintf("floatlab-rqlite-smoke-%d", time.Now().UnixNano())
	const image = "rqlite/rqlite:8.26.0@sha256:b81e945809e1fcc92f3d72c07d7f8726252dfb5ed406db6faaf6879159193fe9"
	docker := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	docker("run", "--detach", "--name", name, "--publish", "127.0.0.1::4001", image, "-node-id=smoke", "-http-addr=0.0.0.0:4001", "-raft-addr=0.0.0.0:4002")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("pinned rqlite diagnostics: %s", out)
		}
		out, err := exec.Command("docker", "rm", "--force", name).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup owned rqlite container: %s %v", out, err)
		}
	})
	address := docker("port", name, "4001/tcp")
	remote := rqlite.NewClient("http://" + address)
	probeClient := &http.Client{Timeout: time.Second}
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/readyz", nil)
		if err != nil {
			t.Fatal(err)
		}
		probe, err := probeClient.Do(request)
		if err == nil {
			probe.Body.Close()
			if probe.StatusCode == 200 {
				break
			}
		}
		if state := docker("inspect", "--format", "{{.State.Running}}", name); state != "true" {
			t.Fatal("pinned rqlite exited before readiness")
		}
		select {
		case <-ctx.Done():
			t.Fatal("pinned rqlite never became ready", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
	oldPath, oldID := workloadDBPath, hostNodeID
	oldUser, oldPass, oldFile, oldRegister := bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode
	workloadDBPath, hostNodeID = filepath.Join(t.TempDir(), "workloads", "local.sqlite"), "smoke-owner"
	bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode = "", "", "", false
	t.Cleanup(func() {
		workloadDBPath, hostNodeID = oldPath, oldID
		bootstrapUser, bootstrapPass, bootstrapFile, registerLocalNode = oldUser, oldPass, oldFile, oldRegister
	})
	for pass := 1; pass <= 2; pass++ {
		if err := initializeLab(ctx, remote); err != nil {
			t.Fatalf("real rqlite migration pass %d: %v", pass, err)
		}
	}
	queries := make([]rqlite.Statement, 0, len(workloadTables)+len(labCacheTables))
	for _, table := range append(append([]string{}, workloadTables...), labCacheTables...) {
		queries = append(queries, rqlite.Statement{SQL: `SELECT * FROM ` + table})
	}
	tables, err := remote.QueryBatch(ctx, queries)
	if err != nil {
		t.Fatal("all source backup/cache tables must exist", err)
	}
	for index, result := range tables {
		if len(result.Columns) == 0 {
			t.Fatalf("missing schema for %s", queries[index].SQL)
		}
	}
	if err = remote.Execute(ctx, nil); err != nil {
		t.Fatal("empty migration batch must be a no-op", err)
	}
	stack := &config.Stack{ID: "web", Name: "web", PrimaryNodeID: hostNodeID, ComposeYAML: "services: {}", FailoverMode: "manual"}
	if err = config.NewStore(remote).CreateStack(ctx, stack); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{1, 0} {
		result, err := remote.Request(ctx, rqlite.Statement{SQL: `UPDATE stacks SET icon='applied' WHERE id='web' AND icon=''`})
		if err != nil || result.RowsAffected != want {
			t.Fatalf("conditional write metadata want%d: %+v %v", want, result, err)
		}
	}
	if err = remote.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO node_pool_ranges VALUES('a',100,200,'node-a')`}}); err != nil {
		t.Fatal(err)
	}
	err = remote.Execute(ctx, []rqlite.Statement{{SQL: `INSERT INTO node_pool_ranges VALUES('b',300,400,'node-b')`}, {SQL: `INSERT INTO node_pool_ranges VALUES('conflict',150,250,'node-c')`}})
	if err == nil {
		t.Fatal("real rqlite accepted overlapping node ranges")
	}
	rows, err := remote.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT count(*) FROM node_pool_ranges WHERE pool_id IN ('b','conflict')`})
	if err != nil || rows.Values[0][0] != float64(0) {
		t.Fatalf("failed range transaction partially committed: %+v %v", rows, err)
	}
	local, err := openWorkloads(ctx, remote)
	if err != nil {
		t.Fatal("real source to native SQLite migration", err)
	}
	defer local.Close()
	var backup workloadBackup
	if err = credentials.ReadPrivate(filepath.Join(filepath.Dir(workloadDBPath), "legacy-rqlite-backup.json"), &backup); err != nil {
		t.Fatal(err)
	}
	if len(backup.Tables) != len(workloadTables) || len(backup.Rows) != len(workloadTables) || backup.NodeID != hostNodeID {
		t.Fatalf("incomplete source backup: %+v", backup)
	}
	if err = rqlite.CopyTables(ctx, remote, local, labCacheTables); err != nil {
		t.Fatal("real transactional cache export", err)
	}
	actual, err := config.NewStore(local).GetStack(ctx, "web")
	if err != nil || actual.Icon != "applied" {
		t.Fatalf("native authority missing migrated rows: %+v %v", actual, err)
	}
	if err = remote.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE stacks SET name='obsolete-source' WHERE id='web'`}}); err != nil {
		t.Fatal(err)
	}
	local.Close()
	local, err = openWorkloads(ctx, remote)
	if err != nil {
		t.Fatal("reopen migrated local authority", err)
	}
	defer local.Close()
	actual, err = config.NewStore(local).GetStack(ctx, "web")
	if err != nil || actual.Name != "web" {
		t.Fatalf("legacy source regained authority: %+v %v", actual, err)
	}
}

func TestRQLiteIntegration(t *testing.T) {
	if os.Getenv("FLOATLAB_RQLITE_INTEGRATION") != "1" {
		t.Skip("run the disposable-appliance integration suite")
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	config, reporter := ginkgo.GinkgoConfiguration()
	config.FailOnEmpty = true
	ginkgo.RunSpecs(t, "TestRQLiteIntegration", config, reporter)
}

var _ = ginkgo.Describe("TestRQLiteIntegration", ginkgo.Serial, func() {
	ginkgo.It("TestPinnedRQLiteMigrationAndLocalAuthority", func() { runTestPinnedRQLiteMigrationAndLocalAuthority(ginkgo.GinkgoT()) })
})
