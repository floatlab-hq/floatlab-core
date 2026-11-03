package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/floatlab/floatlab-core/internal/integrationvm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type testT = GinkgoTInterface

var nodeContext atomic.Pointer[context.Context]

type applianceTestingT struct{ GinkgoTInterface }

// GinkgoT.Context registers cleanup, which cannot be called from cleanup itself.
func (applianceTestingT) Context() context.Context {
	if ctx := nodeContext.Load(); ctx != nil {
		return *ctx
	}
	return context.Background()
}

func integrationT() testT { return applianceTestingT{GinkgoT()} }

func TestApplianceContextFollowsNodeCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	nodeContext.Store(&ctx)
	defer nodeContext.Store(nil)
	helper := applianceTestingT{}
	cancel()
	if helper.Context().Err() != context.Canceled {
		t.Fatal("helper lost the current node's cancellation")
	}
}

type applianceFixture struct {
	run    integrationvm.Run
	image  string
	report string
}

var suiteImage string
var suiteDirectory string

func TestIntegration(t *testing.T) {
	if os.Getenv("FLOATLAB_INTEGRATION") != "1" {
		t.Skip("set FLOATLAB_INTEGRATION=1 on a KVM host")
	}
	RegisterFailHandler(Fail)
	config, reporter := GinkgoConfiguration()
	if config.ParallelTotal != 1 {
		t.Fatal("appliance integration tests run serially")
	}
	config.RandomSeed = 1
	var err error
	reportRoot := env("FLOATLAB_REPORT_DIR", "/tmp")
	if err := os.MkdirAll(reportRoot, 0755); err != nil {
		t.Fatal(err)
	}
	suiteDirectory, err = os.MkdirTemp(reportRoot, "floatlab-reports-")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("integration reports: %s", suiteDirectory)
	reporter.JSONReport = filepath.Join(suiteDirectory, "ginkgo.json")
	reporter.JUnitReport = filepath.Join(suiteDirectory, "junit.xml")
	config.FailOnEmpty = true
	config.Timeout = 11 * time.Hour
	reporter.Verbose = true
	RunSpecs(t, "FloatLab appliance integration", config, reporter, AroundNode(func(ctx context.Context, body func(context.Context)) {
		nodeContext.Store(&ctx)
		defer nodeContext.Store(nil)
		body(ctx)
	}))
}

var _ = BeforeSuite(func(ctx SpecContext) {
	t := integrationT()
	for _, key := range []string{"API_URL", "VM_NAME", "VM_SSH"} {
		Expect(os.Getenv(key)).To(BeEmpty(), "external appliances are no longer supported: unset "+key)
	}
	_, err := os.Stat("/dev/kvm")
	Expect(err).NotTo(HaveOccurred(), "integration tests require KVM")
	root, err := filepath.Abs("..")
	Expect(err).NotTo(HaveOccurred())
	suiteImage = os.Getenv("FLOATLAB_APPLIANCE_ISO")
	if suiteImage != "" {
		return
	}
	host := filepath.Join(suiteDirectory, "host")
	bundle := filepath.Join(suiteDirectory, "bundle")
	iso := filepath.Join(suiteDirectory, "iso")
	buildEnv := []string{"FLOATLAB_INTEGRATION_COVERAGE=1"}
	run(t, root, buildEnv, "nix", "build", "path:"+root+"?dir=floatlab-appliance-image#host-image", "--impure", "--print-build-logs", "--out-link", host)
	run(t, root, buildEnv, "python3", "scripts/release.py", "prepare-iso", bundle, "--version", "integration", "--local-host", host)
	run(t, root, append(buildEnv, "FLOATLAB_ISO_BUNDLE="+bundle), "nix", "build", "path:"+root+"?dir=floatlab-appliance-image#iso", "--impure", "--print-build-logs", "--out-link", iso)
	images, err := filepath.Glob(filepath.Join(iso, "iso", "*.iso"))
	Expect(err).NotTo(HaveOccurred())
	Expect(images).To(HaveLen(1))
	suiteImage, err = filepath.EvalSymlinks(images[0])
	Expect(err).NotTo(HaveOccurred())
})

func (f *applianceFixture) open(name string) {
	f.image = suiteImage
	f.report = filepath.Join(suiteDirectory, name)
	Expect(os.MkdirAll(f.report, 0755)).To(Succeed())
	// Register before Open: a setup failure can leave a network or pool to release.
	DeferCleanup(func() {
		if os.Getenv("FLOATLAB_KEEP_VM") == "1" {
			AddReportEntry("retained resources", f.run.Name+" "+f.run.Directory)
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		Expect(f.run.Close(ctx)).To(Succeed())
	})
	ctx, cancel := context.WithTimeout(integrationT().Context(), time.Minute)
	defer cancel()
	err := f.run.Open(ctx)
	Expect(os.WriteFile(filepath.Join(f.report, "resources.txt"), []byte(f.run.Name+"\n"+f.run.Directory+"\n"), 0600)).To(Succeed())
	Expect(err).NotTo(HaveOccurred())
}

func (f *applianceFixture) collect() {
	var errs []error
	for _, vm := range f.run.Guests {
		out, err := guestCommand(vm, `docker ps -a; journalctl -u floatlab-hostd -u floatlab-core-stack --no-pager -n 100; zfs list -t all`)
		if err != nil {
			out += "\n" + err.Error()
		}
		errs = append(errs, os.WriteFile(filepath.Join(f.report, vm.Name+".log"), []byte(out), 0600))
		errs = append(errs, collectGuestCoverage(vm, filepath.Join(f.report, "coverage", vm.Name)))
	}
	Expect(errors.Join(errs...)).To(Succeed())
}

var _ = DescribeTableSubtree("Multi-node", func(name string) {
	Describe(name, Ordered, Serial, Label("multi-node", name), func() {
		fixture := &applianceFixture{}
		BeforeAll(func(ctx SpecContext) { fixture.open(name) })
		AfterAll(func(ctx SpecContext) { DeferCleanup(fixture.collect) })
		if name == "Lab" {
			defineLab(fixture)
			return
		}
		if name == "Replication" {
			defineReplication(fixture)
		} else {
			defineBackupRestore(fixture)
		}
	})
}, Entry("Lab", "Lab"), Entry("Replication", "Replication"), Entry("BackupRestore", "BackupRestore"))

var _ = Describe("Single-node workflows", Ordered, Serial, Label("single-node"), func() {
	fixture := &applianceFixture{}
	var client *apiClient
	var evidence *coverageRun
	BeforeAll(func(ctx SpecContext) {
		fixture.open("single-node")
		client = startApplianceWithAnswers(integrationT(), fixture, nil)
		evidence = newCoverage(integrationT())
		client.coverage = evidence
	})
	AfterAll(func(ctx SpecContext) {
		// AfterAll precedes per-test deferred cleanups; report their outcome too.
		DeferCleanup(func() {
			if evidence != nil {
				config, _ := GinkgoConfiguration()
				evidence.report(integrationT(), filepath.Join(fixture.report, "endpoints.json"), len(evidence.completed) == 8 && GinkgoLabelFilter() == "" && len(config.FocusStrings) == 0 && len(config.SkipStrings) == 0 && len(config.FocusFiles) == 0 && len(config.SkipFiles) == 0)
			}
		})
	})
	AfterAll(func(ctx SpecContext) { DeferCleanup(fixture.collect) })
	stages := []struct {
		name string
		run  func(testT, *apiClient)
	}{
		{"container-management", runContainerManagementAPI},
		{"cli-caddy", runCLICaddy},
		{"documented-management", func(_ testT, c *apiClient) { c.assertManagementContracts() }},
		{"unconfigured-lab-rejections", func(_ testT, c *apiClient) { c.assertUnconfiguredLab() }},
		{"alert-evaluator", runAlertEvaluator},
		{"persistence-recovery-rollback", func(_ testT, c *apiClient) { c.assertRecoveryWorkload() }},
		{"network-settings", func(_ testT, c *apiClient) { c.assertHostNetworkTransactions() }},
		{"management-update", func(_ testT, c *apiClient) { c.assertManagementUpdateAppliance() }},
	}
	for _, stage := range stages {
		It(stage.name, Label(stage.name), func(ctx SpecContext) {
			client.t = integrationT()
			client.login()
			client.waitNodeReady()
			evidence.begin(stage.name)
			DeferCleanup(func() { evidence.finish(!CurrentSpecReport().Failed()) })
			stage.run(integrationT(), client)
		})
	}
})

var _ = Describe("Installed OS upgrades", Ordered, Serial, Label("host-update"), func() {
	fixture := &applianceFixture{}
	BeforeAll(func(ctx SpecContext) { fixture.open("host-update") })
	AfterAll(func(ctx SpecContext) { DeferCleanup(fixture.collect) })
	It("survives firmware, normal, power and heartbeat fallback, then commits", func(ctx SpecContext) { runHostUpdate(integrationT(), fixture) })
})

func guestCommand(vm *integrationvm.Guest, script string) (string, error) {
	ctx, cancel := context.WithTimeout(integrationT().Context(), 60*time.Second)
	defer cancel()
	if strings.Contains(script, "docker restart") || strings.Contains(script, "systemctl restart") || strings.Contains(script, "systemctl reboot") || strings.Contains(script, "--force-recreate") {
		if out, err := vm.Exec(ctx, checkpointScript); err != nil {
			return out, fmt.Errorf("checkpoint before destructive command: %w", err)
		}
	}
	return vm.Exec(ctx, "export GOCOVERDIR=/floatlab/integration-coverage; "+script)
}

func startApplianceWithAnswers(t testT, fixture *applianceFixture, configuration *installer.Answers) *apiClient {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("%s-%d", fixture.run.Name, len(fixture.run.Guests)+1)
	answers := applianceAnswers(t, name, "demo", "floatlab")
	if configuration != nil {
		answers = *configuration
	}
	dir := t.TempDir()
	if err := installer.WriteAnswers(filepath.Join(dir, "floatlab-answers.toml"), answers); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), "answers.iso")
	run(t, root, nil, "xorriso", "-as", "mkisofs", "-quiet", "-R", "-V", "FLOATLAB_ANSWERS", "-o", iso, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	disk := uint64(32)
	if strings.Contains(fixture.report, "host-update") {
		disk = 64
	}
	vm, err := fixture.run.Boot(ctx, fixture.image, iso, disk)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 20*time.Minute, func() error {
		out, err := guestCommand(vm, `set -eu; if systemctl is-failed --quiet floatlab-unattended-install.service || systemctl is-failed --quiet floatlab-install-discovery.service; then echo INSTALLATION_FAILED; journalctl -u floatlab-unattended-install -u floatlab-install-discovery --no-pager -n 60; exit 1; fi; test "$(findmnt -n -o SOURCE /)" = floatlab/os/live; test -e /floatlab/system/etc/installed; test "$(jq -r .ready /floatlab/system/etc/install-status.json)" = true; test "$(zfs get -H -o value mountpoint floatlab)" = none`)
		if strings.Contains(out, "INSTALLATION_FAILED") {
			t.Fatalf("unattended installation failed: %s", out)
		}
		if err != nil {
			return fmt.Errorf("installation: %s %w", out, err)
		}
		return nil
	})
	c := newAPIClient(t, "http://"+waitForIP(t, vm)+":8080")
	c.vm = vm
	c.waitReady()
	if configuration == nil {
		c.login()
		c.waitNodeReady()
	}
	checkpointGuest(t, vm)
	return c
}
