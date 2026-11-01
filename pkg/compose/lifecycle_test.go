package compose

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseLifecycle(t *testing.T) {
	source := `name: demo
x-fl-health-timeout: 30s
services:
  web:
    image: nginx
    ports: ["8080:80"]
    volumes:
      - type: bind
        source: ./data
        target: /data
        x-fl-recordsize: 64K
        x-fl-snapshots: 1h/2 1d/7
`
	spec, err := ParseLifecycle(source, "Demo")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "demo" || !spec.HasPorts || spec.HealthTimeout.String() != "30s" || spec.Mounts["floatlab/demo/data"].RecordSize != "64K" {
		t.Fatalf("unexpected spec: %#v", spec)
	}
}

func TestExtractProjectRejectsTraversal(t *testing.T) {
	var data bytes.Buffer
	w := tar.NewWriter(&data)
	_ = w.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: 1})
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	dest := t.TempDir()
	if err := ExtractProject(&data, dest); err == nil {
		t.Fatal("traversal accepted")
	}
	if _, err := os.Stat(filepath.Join(dest, "..", "escape")); !os.IsNotExist(err) {
		t.Fatal("escape file created")
	}
}

func TestRuntimeYAMLRewritesMountAndPort(t *testing.T) {
	source := "name: demo\nservices:\n  web:\n    image: nginx\n    ports: [\"8080:80\"]\n    volumes: [\"./data:/data\"]\n"
	runtime, err := RuntimeYAML(source, "demo", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"/floatlab/demo/data", "host_ip: 192.0.2.10"} {
		if !bytes.Contains([]byte(runtime), []byte(wanted)) {
			t.Fatalf("runtime config missing %q:\n%s", wanted, runtime)
		}
	}
}

func TestRuntimeYAMLPreservesServiceNetworking(t *testing.T) {
	source := `name: demo
services:
  web:
    image: caddy:2-alpine
    ports: ["8088:80"]
  probe:
    image: alpine:3.20
`
	runtime, err := RuntimeYAML(source, "demo", "192.0.2.10")
	if err != nil {
		t.Fatal(err)
	}
	project, err := loadProjectAt(runtime, "demo", "/floatlab/demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "probe"} {
		if len(project.Services[name].Networks) != 0 {
			t.Fatalf("%s lost shared default network: %s", name, runtime)
		}
	}
	if project.Services["web"].Ports[0].HostIP != "192.0.2.10" {
		t.Fatal("managed port binding missing")
	}
}

func TestBackupPoliciesAndAliases(t *testing.T) {
	source := `name: demo
services:
  web:
    image: nginx
    volumes:
      - type: bind
        source: ./data
        target: /data
        x-fl-snapshots: h/24 d/28
        x-fl-backups:
          - node_id: node-b
            schedule: h/48 d/90
          - node_id: node-c
            schedule: d/365
`
	spec, err := ParseLifecycle(source, "demo")
	if err != nil {
		t.Fatal(err)
	}
	mount := spec.Mounts["floatlab/demo/data"]
	if len(mount.Backups) != 2 || mount.Backups[1].NodeID != "node-c" || mount.Snapshots[0].Interval != "1h" {
		t.Fatalf("unexpected policies: %+v", mount)
	}
	for _, bad := range []string{strings.Replace(source, "node-c", "node-b", 1), strings.Replace(source, "d/365", "invalid", 1), strings.Replace(source, "h/24 d/28", "h/24 h/48", 1)} {
		if _, err := ParseLifecycle(bad, "demo"); err == nil {
			t.Fatal("accepted invalid backup/snapshot policy")
		}
	}
}
