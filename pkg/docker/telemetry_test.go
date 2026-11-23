package docker

import (
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestDockerTelemetryRetainsScopeAndStreams(t *testing.T) {
	var stats container.StatsResponse
	stats.ID = "container"
	stats.MemoryStats.Usage = 123
	stats.MemoryStats.Stats = map[string]uint64{"inactive_file": 23}
	stats.CPUStats.CPUUsage.TotalUsage = 2e9
	stats.Networks = map[string]container.NetworkStats{"eth0": {RxBytes: 5, TxBytes: 6}}
	stats.MemoryStats.Limit = 1024
	body := dockerMetrics(stats, "stack", "node", "web", "1")
	for _, want := range []string{`stack_id="stack"`, `container_id="container"`, `node_id="node"`, " 123\n", " 2\n", " 5\n", " 6\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	for _, want := range []string{`service="web"`, `replica="1"`, "container_memory_limit_bytes", " 1024\n"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	for _, want := range []string{
		`container_memory_working_set_bytes{stack_id="stack",node_id="node",service="web",replica="1"} 100`,
		`container_memory_enforced_limit_bytes{stack_id="stack",node_id="node",service="web",replica="1"} 1024`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %q in %s", want, body)
		}
	}
	lines := dockerLogLines("2026-10-07T00:00:00Z access marker\n", "stderr", "container", "web", "stack", "node", "caddy")
	if len(lines) != 1 || lines[0]["stream"] != "stderr" || lines[0]["_msg"] != "access marker" || lines[0]["stack_id"] != "stack" {
		t.Fatalf("unexpected logs: %#v", lines)
	}
}
