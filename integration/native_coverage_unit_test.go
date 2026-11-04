package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCoverageRetainsOnlyLatestCheckpointPerProcess(t *testing.T) {
	paths := []string{
		"/floatlab/system/integration-coverage/covcounters.hash.1.300",
		"/floatlab/system/integration-coverage/boot-control-1-10/covmeta.hash",
		"/floatlab/system/integration-coverage/boot-control-1-10/covcounters.hash.1.100",
		"/floatlab/system/integration-coverage/boot-control-1-10/covcounters.hash.1.200",
		"/floatlab/system/integration-coverage/boot-control-1-20/covmeta.hash",
		"/floatlab/system/integration-coverage/boot-control-1-20/covcounters.hash.1.250",
	}
	got, err := latestCoverageFiles(paths)
	want := []string{paths[3], paths[1], paths[5], paths[4]}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("files=%v want=%v error=%v", got, want, err)
	}
	if _, err := latestCoverageFiles(nil); err == nil {
		t.Fatal("empty appliance coverage accepted")
	}
}

func TestCoverageMergeBoundsArgumentsAndRetainsAllCounters(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	if err := os.WriteFile(source, []byte("package main\nfunc main() { println(1) }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "covered")
	if output, err := exec.CommandContext(t.Context(), "go", "build", "-cover", "-covermode=atomic", "-o", binary, source).CombinedOutput(); err != nil {
		t.Fatalf("build coverage fixture: %v: %s", err, output)
	}
	cmd := exec.CommandContext(t.Context(), binary)
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+root)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run coverage fixture: %v: %s", err, output)
	}
	files, err := filepath.Glob(filepath.Join(root, "cov*.*"))
	if err != nil || len(files) != 2 {
		t.Fatalf("coverage fixture files=%v error=%v", files, err)
	}
	const processes = 1500
	var dirs []string
	for i := range processes {
		dir := filepath.Join(root, strings.Repeat("process", 12)+strconv.Itoa(i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if err := os.Link(file, filepath.Join(dir, filepath.Base(file))); err != nil {
				t.Fatal(err)
			}
		}
		dirs = append(dirs, dir)
	}
	if len(strings.Join(dirs, ",")) <= 128<<10 {
		t.Fatal("fixture does not exceed the Linux per-argument limit")
	}
	merged := filepath.Join(root, "merged")
	if err := mergeNativeCoverage(t.Context(), root, dirs, merged); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "coverage.out")
	if output, err := exec.CommandContext(t.Context(), "go", "tool", "covdata", "textfmt", "-i="+merged, "-o="+profile).CombinedOutput(); err != nil {
		t.Fatalf("read merged coverage: %v: %s", err, output)
	}
	data, err := os.ReadFile(profile)
	if err != nil || !strings.Contains(string(data), " 1 "+strconv.Itoa(processes)+"\n") {
		t.Fatalf("counter contributions lost: %s error=%v", data, err)
	}
}
