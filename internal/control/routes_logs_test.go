package control

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/logs"
)

func TestValidateComposeCanonicalizesAndChecksLifecycle(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	canonical, parsed, err := validateCompose(request, `x-fl-stack:
  primary_node: node-a
  storage:
    pool: floatlab
  failover:
    mode: manual
services: {}`, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Extension.PrimaryNode != "node-a" || !strings.Contains(canonical, "name: demo") {
		t.Fatalf("unexpected validation result: %#v\n%s", parsed.Extension, canonical)
	}
}

func TestLogContractHelpers(t *testing.T) {
	start, end, err := logWindow("2026-01-01T00:00:00Z", "2026-01-01T01:00:00Z", time.Time{})
	if err != nil || !start.Before(end) {
		t.Fatalf("logWindow() = %v, %v, %v", start, end, err)
	}
	if _, _, err := logWindow("bad", "", time.Time{}); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
	line := toAPILines([]logs.LogLine{{Time: "2026-01-01T00:00:00Z", Msg: "ready", Stream: map[string]string{"stream": "stderr"}, StackID: "stack-1"}})[0]
	if line.Timestamp == "" || line.Message != "ready" || line.Stream != "stderr" || line.Labels["stack_id"] != "stack-1" {
		t.Fatalf("unexpected API log line: %#v", line)
	}
	if got := logsQLValue(`a\"b`); got != `a\\\"b` {
		t.Fatalf("logsQLValue() = %q", got)
	}
}
