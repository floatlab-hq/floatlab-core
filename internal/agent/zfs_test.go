package agent

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateDataset(t *testing.T) {
	valid := []string{"floatlab", "floatlab/app/db", "pool/a_b-c.d:e"}
	for _, name := range valid {
		if err := validateDataset(name); err != nil {
			t.Fatalf("validateDataset(%q) error = %v", name, err)
		}
	}
	invalid := []string{"", "-pool", "pool//app", "pool/..", "pool/app@snap", "pool/app name"}
	for _, name := range invalid {
		if !errors.Is(validateDataset(name), errBadZFSName) {
			t.Fatalf("validateDataset(%q) did not fail with errBadZFSName", name)
		}
	}
}

func TestParseDatasets(t *testing.T) {
	out := "floatlab\tfilesystem\t/floatlab\t10\t20\t30\t40\tnone\nfloatlab/app\tfilesystem\t/floatlab/app\t1\t2\t3\t4\t100\n"
	datasets, err := parseDatasets(out)
	if err != nil {
		t.Fatalf("parseDatasets() error = %v", err)
	}
	if len(datasets) != 2 {
		t.Fatalf("len(datasets) = %d, want 2", len(datasets))
	}
	if datasets[0].QuotaBytes != nil {
		t.Fatalf("quota = %v, want nil", datasets[0].QuotaBytes)
	}
	if datasets[1].QuotaBytes == nil || *datasets[1].QuotaBytes != 100 {
		t.Fatalf("quota = %v, want 100", datasets[1].QuotaBytes)
	}
}

func TestParseSnapshotsSplitsAtFinalAt(t *testing.T) {
	snapshots, err := parseSnapshots("floatlab/app@backup\t100\t2\t3\n")
	if err != nil {
		t.Fatalf("parseSnapshots() error = %v", err)
	}
	if snapshots[0].Dataset != "floatlab/app" || snapshots[0].Snapshot != "backup" {
		t.Fatalf("snapshot = %+v", snapshots[0])
	}
}

func TestParseDiskstats(t *testing.T) {
	stats := parseDiskstats("8 0 sda 1 0 2 3 4 0 5 6 0 7 8 9 10 11 0 0 0\n")
	if len(stats) != 1 {
		t.Fatalf("len(stats) = %d, want 1", len(stats))
	}
	if stats[0].ReadSectors != 2 || stats[0].WriteSectors != 5 || stats[0].WeightedIOMS != 8 {
		t.Fatalf("stats = %+v", stats[0])
	}
	if !stats[0].HasDiscard || stats[0].DiscardSectors != 11 || !stats[0].HasFlush || stats[0].Flushes != 0 {
		t.Fatalf("stats = %+v", stats[0])
	}
}

func TestMetricsUseFLPrefixAndNodeID(t *testing.T) {
	var b strings.Builder
	metric(&b, "fl_test_metric", labels(map[string]string{"node_id": "node-a"}), 1)
	line := b.String()
	if !strings.HasPrefix(line, "fl_") || !strings.Contains(line, `node_id="node-a"`) {
		t.Fatalf("metric line = %q", line)
	}
}
