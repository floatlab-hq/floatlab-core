package integration_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

var _ = DescribeTableSubtree("Package integration", func(label, pkg, runner, gate string) {
	Describe(label, Ordered, Serial, Label(label), func() {
		fixture := &applianceFixture{}
		var client *apiClient
		BeforeAll(func(ctx SpecContext) {
			fixture.open(label)
			client = startApplianceWithAnswers(integrationT(), fixture, nil)
		})
		AfterAll(func(ctx SpecContext) { DeferCleanup(fixture.collect) })
		It("runs against the real Docker engine and pinned rqlite", func(ctx SpecContext) {
			t := integrationT()
			root, err := filepath.Abs("..")
			if err != nil {
				t.Fatal(err)
			}
			binary := filepath.Join(t.TempDir(), "package-tests")
			run(t, root, []string{"CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64"}, "go", "test", "-c", "-cover", "-covermode=atomic", "-coverpkg=./...", "-tags=integrationcoverage", "-o", binary, pkg)
			const target = "/floatlab/system/updates/package-tests"
			uploadUpgradeGuestFile(t, client.vm, binary, target)
			prefix := target + "-" + label
			script := fmt.Sprintf("mkdir -p /floatlab/integration-coverage/package-%s; (%s=1 %s -test.gocoverdir=/floatlab/integration-coverage/package-%s -test.run='^%s$' -test.v -test.timeout=5m -ginkgo.json-report=%s.json >%s.log 2>&1; echo $? >%s.exit) </dev/null >/dev/null 2>&1 & echo started", label, gate, target, label, runner, prefix, prefix, prefix)
			if out, err := guestCommand(client.vm, script); err != nil {
				t.Fatal(out, err)
			}
			waitFor(t, 6*time.Minute, func() error {
				out, err := guestCommand(client.vm, "if test -f "+prefix+".exit; then cat "+prefix+".exit; else echo running; fi")
				if err != nil {
					return err
				}
				if strings.TrimSpace(out) == "running" {
					return fmt.Errorf("%s still running", label)
				}
				logs, err := guestCommand(client.vm, "cat "+prefix+".log")
				if err != nil {
					t.Fatal(err)
				}
				t.Log(logs)
				if strings.TrimSpace(out) != "0" {
					t.Fatalf("%s exited %s", label, out)
				}
				verifyGuestReport(t, client.vm, prefix+".json")
				return nil
			})
		})
	})
}, Entry("Docker", "docker", "./pkg/docker", "TestDockerIntegration", "FLOATLAB_DOCKER_INTEGRATION"), Entry("rqlite", "rqlite", "./cmd/floatlab-control", "TestRQLiteIntegration", "FLOATLAB_RQLITE_INTEGRATION"))
