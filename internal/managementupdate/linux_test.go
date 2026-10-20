package managementupdate

import (
	"context"
	"errors"
	"fmt"
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

func TestSandboxWaitsForRecoveredDatabaseWithoutWeakeningIsolation(t *testing.T) {
	s := &Status{Manifest: Manifest{Images: map[string]string{"floatlab-control": "control:target", "rqlite": "rqlite:target", "victoria-metrics": "metrics:target", "victoria-logs": "logs:target"}}}
	doc := sandboxCompose(s, "floatlab/system/update-tests/test", "/updates/test/identity.json", "node1", "test-isolated")
	ss := services(doc)
	depends := ss["floatlab-control"]["depends_on"].(map[string]any)
	if depends["rqlite"].(map[string]string)["condition"] != "service_healthy" {
		t.Fatal("control can start before database recovery")
	}
	health := ss["rqlite"]["healthcheck"].(map[string]any)
	if !reflect.DeepEqual(health["test"], []string{"CMD-SHELL", "wget -qO- http://localhost:4001/readyz"}) || health["retries"] != 60 {
		t.Fatalf("database recovery not health gated %v", health)
	}
	for name, service := range ss {
		if service["restart"] != "no" {
			t.Fatalf("%s can restart independently", name)
		}
		if _, ok := service["ports"]; ok {
			t.Fatalf("%s publishes sandbox ports", name)
		}
		for _, v := range service["volumes"].([]any) {
			bind := v.(map[string]any)
			source := bind["source"].(string)
			if strings.Contains(source, "docker.sock") || source == "/run/floatlab" || source == "/floatlab" {
				t.Fatalf("production mount exposed %s", source)
			}
		}
	}
}

func TestSandboxFirewallSeparatesChainsAndCoversBothFamilies(t *testing.T) {
	rules := sandboxFirewallRules("floatlab_test", "fttest")
	if !strings.HasPrefix(rules, "table inet floatlab_test {\n") || !strings.Contains(rules, " }\n chain forward {") || !strings.Contains(rules, "iifname \"fttest\" oifname != \"fttest\" drop\n") {
		t.Fatalf("invalid or incomplete sandbox firewall:\n%s", rules)
	}
	if strings.Contains(rules, "flush") {
		t.Fatal("sandbox must preserve unrelated firewall rules")
	}
}

func TestRestoreUnmountsEntireTreeBeforeRollbackAndRemountsAfterAll(t *testing.T) {
	datasets := []Dataset{{Name: managementDataset}, {Name: managementDataset + "/rqlite"}, {Name: managementDataset + "/raft"}}
	mounted := map[string]bool{}
	for _, ds := range datasets {
		mounted[ds.Name] = true
	}
	removed := false
	rollbacks := 0
	var unmounts, mounts []string
	l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		if name == "docker" {
			if !reflect.DeepEqual(args, []string{"compose", "-p", "floatlab-system", "-f", composePath, "rm", "-f"}) {
				t.Fatalf("unexpected Docker command %v", args)
			}
			removed = true
			return "", nil
		}
		if name == "findmnt" {
			return strings.TrimPrefix(args[4], "/") + " zfs", nil
		}
		switch args[0] {
		case "get":
			if args[4] == "mountpoint,canmount" {
				return "mountpoint\t/" + args[5] + "\ncanmount\ton", nil
			}
			if args[4] == "guid" {
				return "snapshot-guid", nil
			}
			if mounted[args[5]] {
				return "yes", nil
			}
			return "no", nil
		case "unmount":
			if !removed {
				t.Fatal("bind mounts not released")
			}
			mounted[args[1]] = false
			unmounts = append(unmounts, args[1])
		case "rollback":
			for name, yes := range mounted {
				if yes {
					t.Fatalf("rollback with mounted dataset %s", name)
				}
			}
			rollbacks++
		case "mount":
			if rollbacks != len(datasets) {
				t.Fatal("mounted partially restored tree")
			}
			mounted[args[1]] = true
			mounts = append(mounts, args[1])
		default:
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return "", nil
	}}
	if err := l.restoreDatasets(context.Background(), &Status{Datasets: datasets, Snapshot: "owned"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unmounts, []string{datasets[2].Name, datasets[1].Name, datasets[0].Name}) || !reflect.DeepEqual(mounts, []string{datasets[0].Name, datasets[1].Name, datasets[2].Name}) {
		t.Fatalf("unmount %v mount %v", unmounts, mounts)
	}
}

func TestRestoreRefusesBusyUnmountBeforeAnyRollback(t *testing.T) {
	l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		if name == "docker" {
			return "", nil
		}
		if name == "findmnt" {
			return managementDataset + " zfs", nil
		}
		switch args[0] {
		case "get":
			if args[4] == "mountpoint,canmount" {
				return "mountpoint\t/" + args[5] + "\ncanmount\ton", nil
			}
			if args[4] == "guid" {
				return "snapshot-guid", nil
			}
			return "yes", nil
		case "unmount":
			return "", errors.New("dataset busy")
		case "rollback", "mount":
			t.Fatal("advanced after failed unmount")
		}
		return "", nil
	}}
	if err := l.restoreDatasets(context.Background(), &Status{Datasets: []Dataset{{Name: managementDataset}}, Snapshot: "owned"}); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("expected fail-closed busy error %v", err)
	}
}

func TestRestoreRejectsOvermountAndIncompleteUnmount(t *testing.T) {
	for _, overmount := range []bool{true, false} {
		t.Run(fmt.Sprint(overmount), func(t *testing.T) {
			l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
				if name == "docker" {
					return "", nil
				}
				if name == "findmnt" {
					if overmount {
						return "foreign/root tmpfs", nil
					}
					return managementDataset + " zfs", nil
				}
				switch args[0] {
				case "get":
					switch args[4] {
					case "mountpoint,canmount":
						return "mountpoint\t/" + managementDataset + "\ncanmount\ton", nil
					case "guid":
						return "snapshot-guid", nil
					case "mounted":
						return "yes", nil
					}
				case "unmount":
					if overmount {
						t.Fatal("attempted to unmount foreign overmount")
					}
					return "", nil
				case "rollback", "mount":
					t.Fatal("rollback reached without verified offline management tree")
				}
				return "", nil
			}}
			if err := l.restoreDatasets(context.Background(), &Status{Datasets: []Dataset{{Name: managementDataset}}, Snapshot: "owned"}); err == nil {
				t.Fatal("accepted unsafe mount state")
			}
		})
	}
}

func TestRestoreResumesAlreadyUnmountedDataset(t *testing.T) {
	mounted := false
	rolledBack := false
	l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
		if name == "docker" {
			return "", nil
		}
		if name == "findmnt" {
			t.Fatal("unmounted dataset cannot have a mount-source check")
		}
		switch args[0] {
		case "get":
			switch args[4] {
			case "mountpoint,canmount":
				return "mountpoint\t/" + managementDataset + "\ncanmount\ton", nil
			case "guid":
				return "snapshot-guid", nil
			case "mounted":
				if mounted {
					return "yes", nil
				}
				return "no", nil
			}
		case "unmount":
			t.Fatal("already unmounted dataset should be skipped")
		case "rollback":
			rolledBack = true
		case "mount":
			if !rolledBack {
				t.Fatal("mount before rollback")
			}
			mounted = true
		}
		return "", nil
	}}
	if err := l.restoreDatasets(context.Background(), &Status{Datasets: []Dataset{{Name: managementDataset}}, Snapshot: "owned"}); err != nil {
		t.Fatal(err)
	}
	if !mounted || !rolledBack {
		t.Fatal("interrupted rollback was not completed")
	}
}

func TestManagementMountDriftFailsClosed(t *testing.T) {
	for _, scenario := range []struct{ name, properties, mount string }{{"wrong-path", "mountpoint\t/tmp/empty\ncanmount\ton", ""}, {"disabled", "mountpoint\t/" + managementDataset + "\ncanmount\toff", ""}, {"wrong-source", "mountpoint\t/" + managementDataset + "\ncanmount\ton", "floatlab/unrelated zfs"}, {"parent-fallback", "mountpoint\t/" + managementDataset + "\ncanmount\ton", "floatlab/system zfs"}} {
		t.Run(scenario.name, func(t *testing.T) {
			l := &Linux{Run: func(_ context.Context, name string, args ...string) (string, error) {
				if name == "zfs" {
					return scenario.properties, nil
				}
				if name == "findmnt" {
					if args[3] != "--mountpoint" {
						t.Fatal("must require exact mountpoint")
					}
					return scenario.mount, nil
				}
				t.Fatalf("unexpected writer %s", name)
				return "", nil
			}}
			if err := l.verifyMountedDatasets(context.Background(), []Dataset{{Name: managementDataset}}); err == nil {
				t.Fatal("accepted mount drift")
			}
		})
	}
}

func TestManagementBindsRejectPathsOutsideSnapshotTree(t *testing.T) {
	ss := map[string]any{}
	for name, target := range map[string]string{"rqlite": "/rqlite/file", "victoria-metrics": "/victoria-metrics-data", "victoria-logs": "/victoria-logs-data", "floatlab-control": "/var/lib/floatlab/raft"} {
		dataset := name
		if name == "floatlab-control" {
			dataset = "raft"
		}
		ss[name] = map[string]any{"volumes": []any{"/" + managementDataset + "/" + dataset + ":" + target}}
	}
	doc := map[string]any{"services": ss}
	if err := verifyManagementBinds(doc); err != nil {
		t.Fatal(err)
	}
	ss["rqlite"].(map[string]any)["volumes"] = []any{"/tmp/empty:/rqlite/file"}
	if err := verifyManagementBinds(doc); err == nil {
		t.Fatal("accepted unprotected rqlite data bind")
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
