package compose

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
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
	for _, wanted := range []string{"/floatlab/demo/data", "host_ip: 192.0.2.10", "floatlab:"} {
		if !bytes.Contains([]byte(runtime), []byte(wanted)) {
			t.Fatalf("runtime config missing %q:\n%s", wanted, runtime)
		}
	}
}
