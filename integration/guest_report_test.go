package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/internal/integrationvm"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
)

func passedStages(report Report) int {
	passed := 0
	for _, spec := range report.SpecReports {
		if spec.LeafNodeType == types.NodeTypeIt && spec.State == types.SpecStatePassed {
			passed++
		}
	}
	return passed
}

func TestRequiredStageCountsIgnorePassedHooksAndSkippedSpecs(t *testing.T) {
	report := Report{SpecReports: types.SpecReports{
		{LeafNodeType: types.NodeTypeBeforeSuite, State: types.SpecStatePassed},
		{LeafNodeType: types.NodeTypeIt, State: types.SpecStatePassed},
		{LeafNodeType: types.NodeTypeIt, State: types.SpecStateSkipped},
	}}
	if got := passedStages(report); got != 1 {
		t.Fatalf("completed required stages=%d, want 1", got)
	}
	report.PreRunStats.SpecsThatWillRun = 1
	report.SuiteSucceeded = true
	raw, err := json.Marshal([]Report{report})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGuestReport(raw); err != nil {
		t.Fatal(err)
	}
}

func verifyGuestReport(t testT, vm *integrationvm.Guest, source string) {
	t.Helper()
	target := filepath.Join(t.TempDir(), "guest-report.json")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := vm.Download(ctx, source, target); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGuestReport(raw); err != nil {
		t.Fatal(err)
	}
}

func validateGuestReport(raw []byte) error {
	var reports []Report
	if err := json.Unmarshal(raw, &reports); err != nil {
		return err
	}
	if len(reports) != 1 {
		return fmt.Errorf("guest report contains %d suites, want 1", len(reports))
	}
	report := reports[0]
	if !report.SuiteSucceeded || report.PreRunStats.SpecsThatWillRun != 1 || passedStages(report) != 1 {
		return fmt.Errorf("guest suite did not execute its required stage: succeeded=%v selected=%d passed=%d", report.SuiteSucceeded, report.PreRunStats.SpecsThatWillRun, passedStages(report))
	}
	return nil
}

var _ = ReportAfterSuite("required integration stages", func(report Report) {
	if report.SuiteSucceeded {
		Expect(passedStages(report)).To(Equal(report.PreRunStats.SpecsThatWillRun), "required integration stages must not skip")
	}
})
