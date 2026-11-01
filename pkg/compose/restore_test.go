package compose

import (
	"context"
	"strings"
	"testing"
)

func TestRestoreSourceIsolatesIdentityAndPreservesManagedData(t *testing.T) {
	source := `name: original
x-fl-stack:
  id: original-id
  primary_node: source
  secondary_node: backup
  failover: {mode: auto}
  replication: {enabled: true}
  backup: {enabled: true, target: backup}
  storage: {pool: floatlab, compression: lz4}
services:
  app:
    image: nginx:alpine
    container_name: original-app
    environment: {CONTENT_PATH: /data}
    volumes:
      - type: bind
        source: /floatlab/original/content/data
        target: /data
        x-fl-snapshots: h/24 d/28
        x-fl-backups:
          - {node_id: backup, schedule: h/48}
      - cache:/cache
      - /etc/localtime:/etc/localtime:ro
volumes:
  cache: {}
`
	result, err := RestoreSource(source, "original", "Restored Copy", "destination")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseLifecycle(result, "restored-copy")
	if err != nil {
		t.Fatal(err)
	}
	data := spec.Mounts["floatlab/restored-copy/data"]
	if data.Source != "content/data" || data.Target != "/data" || len(data.Snapshots) != 2 || len(data.Backups) != 0 {
		t.Fatalf("data layout or policy changed: %+v", data)
	}
	if spec.Mounts["floatlab/restored-copy/cache"].Target != "/cache" {
		t.Fatal("named managed mount lost")
	}
	if spec.Project.Services["app"].Environment["CONTENT_PATH"] == nil || *spec.Project.Services["app"].Environment["CONTENT_PATH"] != "/data" {
		t.Fatal("service configuration changed")
	}
	if spec.Project.Services["app"].ContainerName != "" {
		t.Fatal("restored service retains globally conflicting container name")
	}
	parsed, err := Parse(context.Background(), result, "restored-copy")
	if err != nil {
		t.Fatal(err)
	}
	ext := parsed.Extension
	if ext.PrimaryNode != "destination" || ext.ID != "" || ext.SecondaryNode != "" || ext.Failover.Mode != "" || ext.Replication.Enabled || ext.Backup.Enabled || ext.Storage.Pool != "floatlab" {
		t.Fatalf("restored identity not isolated: %+v", ext)
	}
	if strings.Contains(result, "x-fl-backups") || strings.Contains(result, "/floatlab/original/") {
		t.Fatalf("original backup or mount references remain: %s", result)
	}
}

func TestRestoreSourceRejectsAssetsAbsentFromBackups(t *testing.T) {
	for name, source := range map[string]string{
		"build":                 "services:\n  app:\n    image: alpine\n    build: ./app\n",
		"env file":              "services:\n  app:\n    image: alpine\n    env_file: .env\n",
		"label file":            "services:\n  app:\n    image: alpine\n    label_file: labels\n",
		"extends":               "services:\n  app:\n    image: alpine\n    extends: {file: base.yml, service: app}\n",
		"include":               "include: [other.yml]\nservices:\n  app: {image: alpine}\n",
		"config file":           "services:\n  app: {image: alpine}\nconfigs:\n  settings: {file: ./settings.conf}\n",
		"secret file":           "services:\n  app: {image: alpine}\nsecrets:\n  password: {file: ./password}\n",
		"readonly short bind":   "services:\n  app:\n    image: alpine\n    volumes: ['./settings:/settings:ro']\n",
		"readonly long bind":    "services:\n  app:\n    image: alpine\n    volumes:\n      - {type: bind, source: ./settings, target: /settings, read_only: true}\n",
		"readonly named volume": "services:\n  app:\n    image: alpine\n    volumes: ['settings:/settings:ro']\nvolumes:\n  settings: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := RestoreSource(source, "original", "restored", "destination"); err == nil || !strings.Contains(err.Error(), "cannot preserve") {
				t.Fatalf("missing assets accepted or unclear error: %v", err)
			}
		})
	}
}

func TestRestoreSourcePreservesInlineConfigAndShortBind(t *testing.T) {
	source := `services:
  app:
    image: alpine
    volumes: [/floatlab/original/data:/data]
    configs: [settings]
configs:
  settings:
    content: inline-content
`
	result, err := RestoreSource(source, "original", "restored", "destination")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "./data:/data") || !strings.Contains(result, "inline-content") {
		t.Fatalf("restorable content changed: %s", result)
	}
}

func TestRestoreSourceRejectsInvalidIdentity(t *testing.T) {
	for _, args := range [][3]string{{"original", "original", "node"}, {"original", "restored", ""}, {"original", "!!!", "node"}} {
		if _, err := RestoreSource("services:\n  app: {image: alpine}\n", args[0], args[1], args[2]); err == nil {
			t.Fatalf("invalid identity accepted: %v", args)
		}
	}
}
