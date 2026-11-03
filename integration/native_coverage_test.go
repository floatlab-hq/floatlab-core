package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/integrationvm"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const checkpointScript = `set -eu
for process in /proc/[0-9]*; do
 executable=$(readlink "$process/exe" 2>/dev/null || true)
 case "${executable##*/}" in floatlab-control|floatlab-hostd|appliance-hostd-current|appliance-management-candidate)
  pid=${process##*/}
  namespace_pid=$(awk '/^NSpid:/ {print $NF}' "$process/status")
  test -n "$namespace_pid" || namespace_pid=$pid
  start=$(awk '{print $22}' "$process/stat")
  directory=$(find /floatlab/integration-coverage /run/floatlab-boot-recovery/coverage -mindepth 1 -maxdepth 1 -type d -name "*-$namespace_pid-$start" 2>/dev/null | head -n 1)
  test -n "$directory"
  rm -f "$directory/checkpoint"
  kill -USR2 "$pid"
  for attempt in $(seq 1 100); do test ! -e "$directory/checkpoint" || break; sleep 0.1; done
  test -e "$directory/checkpoint"
 ;; esac
done
if test -d /run/floatlab-boot-recovery/coverage; then cp -a /run/floatlab-boot-recovery/coverage/. /floatlab/integration-coverage/; fi`

func checkpointGuest(t testT, vm *integrationvm.Guest) {
	t.Helper()
	out, err := guestCommand(vm, checkpointScript)
	if err != nil {
		t.Fatalf("coverage checkpoint: %s %v", out, err)
	}
}

func collectGuestCoverage(vm *integrationvm.Guest, directory string) error {
	if _, err := guestCommand(vm, checkpointScript); err != nil {
		return err
	}
	out, err := guestCommand(vm, `find /floatlab/integration-coverage -type f \( -name 'covmeta.*' -o -name 'covcounters.*' \)`)
	if err != nil {
		return err
	}
	files, err := latestCoverageFiles(strings.Fields(out))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(integrationT().Context(), 5*time.Minute)
	defer cancel()
	for _, source := range files {
		rel, err := filepath.Rel("/floatlab/integration-coverage", source)
		if err != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("invalid coverage path %q", source)
		}
		target := filepath.Join(directory, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		if err := vm.Download(ctx, source, target); err != nil {
			return err
		}
	}
	return nil
}

func latestCoverageFiles(paths []string) ([]string, error) {
	latest := map[string]string{}
	for _, path := range paths {
		// The runtime also writes root-level exit profiles. The hook's per-boot,
		// per-process checkpoints avoid PID collisions across guest reboots.
		if filepath.Dir(path) == "/floatlab/integration-coverage" {
			continue
		}
		name := filepath.Base(path)
		key := path
		if strings.HasPrefix(name, "covcounters.") {
			parts := strings.Split(name, ".")
			if len(parts) != 4 {
				return nil, fmt.Errorf("invalid counter filename %q", name)
			}
			key = filepath.Join(filepath.Dir(path), strings.Join(parts[:3], "."))
		}
		if latest[key] < path {
			latest[key] = path
		}
	}
	if len(latest) == 0 {
		return nil, errors.New("instrumented appliance emitted no coverage")
	}
	files := make([]string, 0, len(latest))
	for _, path := range latest {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

var _ = AfterSuite(func(ctx SpecContext) {
	var dirs []string
	err := filepath.WalkDir(suiteDirectory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "covcounters.") {
			dirs = append(dirs, filepath.Dir(path))
		}
		return nil
	})
	Expect(err).NotTo(HaveOccurred())
	sort.Strings(dirs)
	dirs = compactStrings(dirs)
	if len(dirs) == 0 {
		return
	}
	merged := filepath.Join(suiteDirectory, "merged")
	Expect(os.MkdirAll(merged, 0755)).To(Succeed())
	t := integrationT()
	root, err := filepath.Abs("..")
	Expect(err).NotTo(HaveOccurred())
	run(t, root, nil, "go", "tool", "covdata", "merge", "-i="+strings.Join(dirs, ","), "-o="+merged)
	run(t, root, nil, "go", "tool", "covdata", "percent", "-i="+merged)
	profile := filepath.Join(suiteDirectory, "coverage.out")
	run(t, root, nil, "go", "tool", "covdata", "textfmt", "-i="+merged, "-o="+profile)
	run(t, root, nil, "go", "tool", "cover", "-html="+profile, "-o="+filepath.Join(suiteDirectory, "coverage.html"))
})

func compactStrings(values []string) []string {
	var out []string
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}
