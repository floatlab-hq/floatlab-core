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
	root, services, _, err := readRestoreSource(source, oldSlug)
	if err != nil {
		return "", err
	}
	oldDir := filepath.Join("/floatlab", oldSlug)
	for _, service := range services {
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

// ParseRestoreSource requires all project assets to be available from Compose and backed-up mounts.
func ParseRestoreSource(source, name string) (*LifecycleSpec, error) {
	_, _, spec, err := readRestoreSource(source, name)
	return spec, err
}

func readRestoreSource(source, slug string) (map[string]any, map[string]map[string]any, *LifecycleSpec, error) {
	var root map[string]any
	if err := yaml.Unmarshal([]byte(source), &root); err != nil || root == nil {
		return nil, nil, nil, fmt.Errorf("compose: invalid restore source")
	}
	if _, ok := root["include"]; ok {
		return nil, nil, nil, fmt.Errorf("compose: restore cannot preserve included project files")
	}
	rawServices, ok := root["services"].(map[string]any)
	if !ok {
		return nil, nil, nil, fmt.Errorf("compose: restore requires services")
	}
	services := make(map[string]map[string]any, len(rawServices))
	for name, raw := range rawServices {
		service, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, nil, fmt.Errorf("compose: invalid service %s", name)
		}
		for _, key := range []string{"build", "env_file", "label_file"} {
			if _, found := service[key]; found {
				return nil, nil, nil, fmt.Errorf("compose: restore cannot preserve service %s %s assets", name, key)
			}
		}
		if extends, ok := service["extends"].(map[string]any); ok && extends["file"] != nil {
			return nil, nil, nil, fmt.Errorf("compose: restore cannot preserve service %s extends file", name)
		}
		services[name] = service
	}
	for _, key := range []string{"configs", "secrets"} {
		definitions, _ := root[key].(map[string]any)
		for name, raw := range definitions {
			definition, _ := raw.(map[string]any)
			if definition["file"] != nil {
				return nil, nil, nil, fmt.Errorf("compose: restore cannot preserve %s %s file", key, name)
			}
		}
	}
	spec, err := ParseLifecycle(source, slug)
	if err != nil {
		return nil, nil, nil, err
	}
	oldDir := filepath.Join("/floatlab", slug)
	for name, service := range spec.Project.Services {
		for _, volume := range service.Volumes {
			if !volume.ReadOnly || volume.Type == "tmpfs" {
				continue
			}
			if volume.Type == "volume" || (volume.Type == "bind" && withinProject(oldDir, volume.Source)) {
				return nil, nil, nil, fmt.Errorf("compose: restore cannot preserve service %s read-only project mount %s", name, volume.Source)
			}
		}
	}
	return root, services, spec, nil
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
