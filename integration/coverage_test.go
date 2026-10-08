package integration_test

import (
	"encoding/json"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/floatlab/floatlab-core/api/openapi"
)

type endpointEvidence struct {
	Operation  openapi.Operation `json:"operation"`
	Successful []string          `json:"successful_scenarios"`
	Rejection  []string          `json:"rejection_scenarios"`
	Deferred   []string          `json:"deferred_scenarios"`
}
type coverageRun struct {
	mu         sync.Mutex
	operations []openapi.Operation
	scenario   string
	staged     map[string]map[int]bool
	evidence   map[string]*endpointEvidence
}

// Only TestWorkloadConfidence enables this observer; integration tests are serial.
var activeCoverage *coverageRun

func newCoverage(t *testing.T) *coverageRun {
	t.Helper()
	operations, err := openapi.ManagementOperations()
	if err != nil {
		t.Fatal(err)
	}
	run := &coverageRun{operations: operations, evidence: map[string]*endpointEvidence{}}
	for _, op := range operations {
		entry := &endpointEvidence{Operation: op, Successful: []string{}, Rejection: []string{}, Deferred: []string{}}
		if multiNodeOperation(op) {
			entry.Deferred = []string{"real multi-node success"}
		}
		run.evidence[op.Method+" "+op.Path] = entry
	}
	return run
}
func multiNodeOperation(op openapi.Operation) bool {
	return op.Method == "POST" && (strings.Contains(op.Path, "/failover") || op.Path == "/api/v1/stacks/{id}/restore" || strings.Contains(op.Path, "/storage/replication/"))
}
func matchesPath(pattern, path string) bool {
	a, b := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !(strings.HasPrefix(a[i], "{") && strings.HasSuffix(a[i], "}")) {
			return false
		}
	}
	return true
}
func (run *coverageRun) observe(method, path string, status int) {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.staged == nil || status == 401 {
		return
	}
	u, err := url.Parse(path)
	if err != nil {
		return
	}
	key := ""
	specificity := -1
	for _, op := range run.operations {
		if op.Method == method && matchesPath(op.Path, u.Path) {
			score := len(op.Path) - strings.Count(op.Path, "{")*100
			if score > specificity || key == "" {
				specificity = score
				key = op.Method + " " + op.Path
			}
		}
	}
	if key == "" {
		return
	}
	if run.staged[key] == nil {
		run.staged[key] = map[int]bool{}
	}
	run.staged[key][status] = true
}
func (run *coverageRun) scenarioTest(t *testing.T, name string, fn func(*testing.T)) bool {
	run.mu.Lock()
	run.scenario = name
	run.staged = map[string]map[int]bool{}
	run.mu.Unlock()
	skipped := false
	passed := t.Run(name, func(child *testing.T) { defer func() { skipped = child.Skipped() }(); fn(child) })
	if skipped {
		t.Errorf("required scenario skipped: %s", name)
		passed = false
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	if passed {
		for key, statuses := range run.staged {
			for status := range statuses {
				entry := run.evidence[key]
				if status >= 200 && status < 300 || status == 101 {
					entry.Successful = appendUnique(entry.Successful, name)
				} else if status >= 400 {
					entry.Rejection = appendUnique(entry.Rejection, name)
				}
			}
		}
	}
	run.staged = nil
	return passed
}
func appendUnique(values []string, value string) []string {
	for _, old := range values {
		if old == value {
			return values
		}
	}
	return append(values, value)
}
func (run *coverageRun) report(t *testing.T) {
	t.Helper()
	run.mu.Lock()
	defer run.mu.Unlock()
	entries := []*endpointEvidence{}
	missing := []string{}
	for key, entry := range run.evidence {
		entries = append(entries, entry)
		covered := len(entry.Successful) > 0
		if multiNodeOperation(entry.Operation) {
			covered = covered || len(entry.Rejection) > 0
		}
		if entry.Operation.ID == "getFailoverLog" {
			covered = covered || len(entry.Rejection) > 0
			entry.Deferred = []string{"real multi-node step history"}
		}
		if !covered {
			missing = append(missing, key)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Operation.Path+entries[i].Operation.Method < entries[j].Operation.Path+entries[j].Operation.Method
	})
	sort.Strings(missing)
	payload, err := json.MarshalIndent(map[string]interface{}{"passed": !t.Failed() && len(missing) == 0, "total": len(entries), "covered": len(entries) - len(missing), "missing": missing, "operations": entries}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if path := os.Getenv("FLOATLAB_COVERAGE_REPORT"); path != "" {
		if err := os.WriteFile(path, payload, 0644); err != nil {
			t.Error(err)
		}
	}
	t.Logf("API endpoint coverage: %d/%d; multi-node success explicitly deferred", len(entries)-len(missing), len(entries))
	if len(missing) > 0 {
		t.Errorf("uncovered required API operations:\n%s", strings.Join(missing, "\n"))
	}
}
