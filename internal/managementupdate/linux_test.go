package managementupdate

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestFreezeDefinitionsUsesRunningImageAndActualRestartPolicy(t *testing.T) {
	actual := "sha256:" + strings.Repeat("a", 64)
	var tags []string
	l := &Linux{Run: func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "ps":
			return "running-container", nil
		case "inspect":
			if args[2] == "{{.Image}}" {
				return actual, nil
			}
			return `{"Name":"on-failure","MaximumRetryCount":7}`, nil
		case "tag":
			tags = append(tags, args[1]+" "+args[2])
			return "", nil
		}
		t.Fatalf("unexpected args %v", args)
		return "", nil
	}}
	doc := map[string]any{"services": map[string]any{"floatlab-control": map[string]any{"image": "mutable:latest", "restart": "always"}}}
	s := &Status{}
	if e := l.freezeDefinitions(context.Background(), s, doc); e != nil {
		t.Fatal(e)
	}
	expected := "floatlab-rollback/floatlab-control:" + strings.Repeat("a", 64)
	if services(doc)["floatlab-control"]["image"] != expected || s.Policies["floatlab-control"] != "on-failure:7" || len(tags) != 1 || tags[0] != actual+" "+expected {
		t.Fatalf("image %v policy %v tags %v", services(doc), s.Policies, tags)
	}
}

func TestSandboxMemoryPreservesHostHeadroom(t *testing.T) {
	for _, input := range []string{"", "MemAvailable: nope kB", "MemAvailable: 4718591 kB\n", "MemAvailable: 123 MB\n"} {
		if e := checkSandboxMemory(input); e == nil {
			t.Fatalf("accepted unsafe memory %q", input)
		}
	}
	if e := checkSandboxMemory("MemAvailable: 4718592 kB\n"); e != nil {
		t.Fatal(e)
	}
}

func TestSnapshotIncludesEveryManagementChildAndHoldsRollback(t *testing.T) {
	var commands []string
	l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, name+" "+strings.Join(args, " "))
		if name == "zfs" && args[0] == "list" {
			return managementDataset + " 1\n" + managementDataset + "/rqlite 2\n" + managementDataset + "/raft 3\n" + managementDataset + "/victoria-metrics 4\n" + managementDataset + "/victoria-logs 5", nil
		}
		return "", nil
	}}
	s := &Status{OperationID: "test", Datasets: []Dataset{{Name: managementDataset}, {Name: managementDataset + "/rqlite"}, {Name: managementDataset + "/raft"}}}
	if e := l.Snapshot(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	want := []string{"docker ps -q --filter label=com.docker.compose.project=floatlab-system", "zfs list -H -r -t filesystem -o name,guid floatlab/system/management", "zfs snapshot -r floatlab/system/management@floatlab-update-test", "zfs hold floatlab-update floatlab/system/management@floatlab-update-test", "zfs hold floatlab-update floatlab/system/management/rqlite@floatlab-update-test", "zfs hold floatlab-update floatlab/system/management/raft@floatlab-update-test", "zfs hold floatlab-update floatlab/system/management/victoria-metrics@floatlab-update-test", "zfs hold floatlab-update floatlab/system/management/victoria-logs@floatlab-update-test"}
	if !reflect.DeepEqual(commands, want) {
		t.Fatalf("commands %v", commands)
	}
}
func TestSnapshotRefusesRunningWriter(t *testing.T) {
	l := &Linux{Run: func(_ context.Context, name string, _ ...string) (string, error) {
		if name == "docker" {
			return "still-running", nil
		}
		t.Fatal("snapshot attempted with live writer")
		return "", nil
	}}
	if e := l.Snapshot(context.Background(), &Status{OperationID: "test"}); e == nil {
		t.Fatal("accepted live writer")
	}
}
func TestSnapshotHoldFailureIsNotSuccess(t *testing.T) {
	l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		if name == "zfs" && args[0] == "list" {
			return managementDataset + " 1\n" + managementDataset + "/rqlite 2\n" + managementDataset + "/raft 3\n" + managementDataset + "/victoria-metrics 4\n" + managementDataset + "/victoria-logs 5", nil
		}
		if name == "zfs" && args[0] == "hold" {
			return "", errors.New("hold failed")
		}
		return "", nil
	}}
	s := &Status{OperationID: "test", Datasets: []Dataset{{Name: managementDataset}}}
	if e := l.Snapshot(context.Background(), s); e == nil || !strings.Contains(e.Error(), "hold failed") {
		t.Fatal("ignored hold failure")
	}
}
