package compose

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RestoreSource isolates a restored workload while preserving its managed mount layout.
func RestoreSource(source, oldName, newName, destNodeID string) (string, error) {
	oldSlug, err := Slug(oldName)
	if err != nil {
		return "", err
	}
	newSlug, err := Slug(newName)
	if err != nil {
		return "", err
	}
	if oldSlug == newSlug || strings.TrimSpace(destNodeID) == "" {
		return "", fmt.Errorf("compose: restore requires a new stack name and destination node")
	}
	var root map[string]any
	if err := yaml.Unmarshal([]byte(source), &root); err != nil || root == nil {
		return "", fmt.Errorf("compose: invalid restore source")
	}
	if _, ok := root["include"]; ok {
		return "", fmt.Errorf("compose: restore cannot preserve included project files")
	}
	services, ok := root["services"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("compose: restore requires services")
	}
	for name, raw := range services {
		service, ok := raw.(map[string]any)
		if !ok {
			return "", fmt.Errorf("compose: invalid service %s", name)
		}
		for _, key := range []string{"build", "env_file", "label_file"} {
			if _, found := service[key]; found {
				return "", fmt.Errorf("compose: restore cannot preserve service %s %s assets", name, key)
			}
		}
		if extends, ok := service["extends"].(map[string]any); ok && extends["file"] != nil {
			return "", fmt.Errorf("compose: restore cannot preserve service %s extends file", name)
		}
	}
	for _, key := range []string{"configs", "secrets"} {
		definitions, _ := root[key].(map[string]any)
		for name, raw := range definitions {
			definition, _ := raw.(map[string]any)
			if definition["file"] != nil {
				return "", fmt.Errorf("compose: restore cannot preserve %s %s file", key, name)
			}
		}
	}
	spec, err := ParseLifecycle(source, oldSlug)
	if err != nil {
		return "", err
	}
	oldDir := filepath.Join("/floatlab", oldSlug)
	for name, service := range spec.Project.Services {
		for _, volume := range service.Volumes {
			if !volume.ReadOnly || volume.Type == "tmpfs" {
				continue
			}
			if volume.Type == "volume" || (volume.Type == "bind" && withinProject(oldDir, volume.Source)) {
				return "", fmt.Errorf("compose: restore cannot preserve service %s read-only project mount %s", name, volume.Source)
			}
		}
	}
	for _, raw := range services {
		service := raw.(map[string]any)
		delete(service, "container_name")
		volumes, _ := service["volumes"].([]any)
		for i, raw := range volumes {
			switch volume := raw.(type) {
			case string:
				parts := strings.SplitN(volume, ":", 2)
				if len(parts) == 2 && filepath.IsAbs(parts[0]) && withinProject(oldDir, parts[0]) {
					relative, _ := filepath.Rel(oldDir, parts[0])
					volumes[i] = "./" + relative + ":" + parts[1]
				}
			case map[string]any:
				if path, ok := volume["source"].(string); ok && filepath.IsAbs(path) && withinProject(oldDir, path) {
					relative, _ := filepath.Rel(oldDir, path)
					volume["source"] = "./" + relative
				}
			}
		}
	}
	root["name"] = newSlug
	ext, ok := root[stackExtKey].(map[string]any)
	if root[stackExtKey] != nil && !ok {
		return "", fmt.Errorf("compose: invalid %s", stackExtKey)
	}
	if ext == nil {
		ext = map[string]any{}
		root[stackExtKey] = ext
	}
	ext["primary_node"] = destNodeID
	for _, key := range []string{"id", "secondary_node", "failover", "replication", "backup"} {
		delete(ext, key)
	}
	removeRestoreBackupTargets(root)
	data, err := yaml.Marshal(root)
	if err != nil {
		return "", err
	}
	if _, err = ParseLifecycle(string(data), newSlug); err != nil {
		return "", err
	}
	return string(data), nil
}

func withinProject(directory, path string) bool {
	relative, err := filepath.Rel(directory, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeRestoreBackupTargets(value any) {
	switch node := value.(type) {
	case map[string]any:
		delete(node, "x-fl-backups")
		for _, child := range node {
			removeRestoreBackupTargets(child)
		}
	case []any:
		for _, child := range node {
			removeRestoreBackupTargets(child)
		}
	}
}
