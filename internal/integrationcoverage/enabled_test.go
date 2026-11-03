//go:build integrationcoverage

package integrationcoverage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGuardCheckpointCopyReplacesEarlierCounters(t *testing.T) {
	source, target := t.TempDir(), filepath.Join(t.TempDir(), "checkpoint")
	path := filepath.Join(source, "covcounters.hash.1.1")
	for _, value := range []string{"before-expiry", "after-expiry"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if err := copyCheckpoint(source, target); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(target, filepath.Base(path)))
		if err != nil || string(data) != value {
			t.Fatalf("checkpoint=%q error=%v", data, err)
		}
	}
}
