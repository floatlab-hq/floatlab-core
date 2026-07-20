package compose

import (
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

const DefaultHealthTimeout = 2 * time.Minute

var (
	slugPattern     = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	snapshotPattern = regexp.MustCompile(`^([1-9][0-9]*)(m|h|d|w|mo|y)/([1-9][0-9]*)$`)
	zfsSizePattern  = regexp.MustCompile(`^[1-9][0-9]*([KMGTPE]i?B?|[kmgtpe])?$`)
)

type LifecycleSpec struct {
	Name          string
	NetworkPool   string
	HealthTimeout time.Duration
	Alerts        []LifecycleAlert
	Mounts        map[string]Mount
	HasPorts      bool
	Project       *types.Project
}

type LifecycleAlert struct {
	Name, Metric, Service, Mount, Comparator, Duration, Severity, Message string
	Threshold                                                             float64
}

type Mount struct {
	Dataset, Source, Target, Type, RecordSize, Compression, Quota string
	Snapshots                                                     []SnapshotTier
}

type SnapshotTier struct {
	Interval string
	Retain   int
}

func Slug(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(name, "-")
	name = strings.Trim(name, "-")
	if name == "" || len(name) > 63 || !slugPattern.MatchString(name) {
		return "", fmt.Errorf("compose: invalid stack name")
	}
	return name, nil
}

func ParseLifecycle(source, requestedName string) (*LifecycleSpec, error) {
	return ParseLifecycleAt(source, requestedName, filepath.Join("/floatlab", requestedName))
}

// ParseLifecycleAt validates a project while resolving its local references from workingDir.
func ParseLifecycleAt(source, requestedName, workingDir string) (*LifecycleSpec, error) {
	slug, err := Slug(requestedName)
	if err != nil {
		return nil, err
	}
	project, err := loadProjectAt(source, slug, workingDir)
	if err != nil {
		return nil, err
	}
	if project.Name != slug {
		return nil, fmt.Errorf("compose: name must be %q", slug)
	}
	spec := &LifecycleSpec{Name: slug, HealthTimeout: DefaultHealthTimeout, Mounts: map[string]Mount{}, Project: project}
	if value, ok := project.Extensions["x-fl-network-pool"].(string); ok {
		spec.NetworkPool = value
	}
	if value, ok := project.Extensions["x-fl-health-timeout"].(string); ok && value != "" {
		spec.HealthTimeout, err = time.ParseDuration(value)
		if err != nil || spec.HealthTimeout <= 0 {
			return nil, fmt.Errorf("compose: invalid x-fl-health-timeout")
		}
	}
	if err := decodeExtension(project.Extensions["x-fl-alert-rules"], &spec.Alerts); err != nil {
		return nil, fmt.Errorf("compose: x-fl-alert-rules: %w", err)
	}
	if err := validateAlerts(spec.Alerts); err != nil {
		return nil, err
	}
	for serviceName, service := range project.Services {
		if service.NetworkMode == "host" {
			return nil, fmt.Errorf("compose: service %s uses forbidden network_mode host", serviceName)
		}
		if len(service.Ports) > 0 {
			spec.HasPorts = true
		}
		for _, volume := range service.Volumes {
			if volume.ReadOnly || volume.Type == "tmpfs" {
				continue
			}
			if volume.Type == "volume" && bool(project.Volumes[volume.Source].External) {
				return nil, fmt.Errorf("compose: service %s uses writable external volume %s", serviceName, volume.Source)
			}
			mount, err := lifecycleMount(slug, workingDir, volume)
			if err != nil {
				return nil, fmt.Errorf("compose: service %s mount %s: %w", serviceName, volume.Target, err)
			}
			if prior, exists := spec.Mounts[mount.Dataset]; exists && !reflect.DeepEqual(prior, mount) {
				return nil, fmt.Errorf("compose: conflicting options for shared mount %s", mount.Source)
			}
			spec.Mounts[mount.Dataset] = mount
		}
	}
	return spec, nil
}

// RuntimeYAML returns a runtime-only Compose model with managed mountpoints and port bindings.
func RuntimeYAML(source, requestedName, stackIP string) (string, error) {
	spec, err := ParseLifecycle(source, requestedName)
	if err != nil {
		return "", err
	}
	if spec.HasPorts && !validateIPv4(stackIP) {
		return "", fmt.Errorf("compose: valid stack IPv4 address is required for published ports")
	}
	project := spec.Project
	if spec.HasPorts {
		if project.Networks == nil {
			project.Networks = types.Networks{}
		}
		project.Networks["floatlab"] = types.NetworkConfig{Driver: "bridge"}
	}
	for name, service := range project.Services {
		for i, volume := range service.Volumes {
			if volume.ReadOnly || volume.Type == "tmpfs" {
				continue
			}
			mount, err := lifecycleMount(spec.Name, filepath.Join("/floatlab", spec.Name), volume)
			if err != nil {
				return "", err
			}
			service.Volumes[i].Type = "bind"
			service.Volumes[i].Source = "/" + mount.Dataset
		}
		if len(service.Ports) > 0 {
			if service.Networks == nil {
				service.Networks = map[string]*types.ServiceNetworkConfig{}
			}
			service.Networks["floatlab"] = nil
			for i := range service.Ports {
				service.Ports[i].HostIP = stackIP
			}
		}
		project.Services[name] = service
	}
	data, err := yaml.Marshal(project)
	if err != nil {
		return "", fmt.Errorf("compose: marshal runtime project: %w", err)
	}
	return string(data), nil
}

func CanonicalSource(source, requestedName string) (string, error) {
	slug, err := Slug(requestedName)
	if err != nil {
		return "", err
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil || len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose: invalid YAML document")
	}
	root := document.Content[0]
	name := mappingValue(root, "name")
	if name == nil {
		root.Content = append([]*yaml.Node{{Kind: yaml.ScalarNode, Value: "name"}, {Kind: yaml.ScalarNode, Value: slug}}, root.Content...)
	} else if name.Value != slug {
		return "", fmt.Errorf("compose: name must be %q", slug)
	}
	data, err := yaml.Marshal(&document)
	return string(data), err
}

func lifecycleMount(stack, workingDir string, volume types.ServiceVolumeConfig) (Mount, error) {
	if volume.Type == "volume" {
		if definition, ok := volume.Extensions["external"].(bool); ok && definition {
			return Mount{}, fmt.Errorf("writable external volumes are forbidden")
		}
	} else if volume.Type == "bind" {
		if filepath.IsAbs(volume.Source) {
			relative, err := filepath.Rel(workingDir, volume.Source)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return Mount{}, fmt.Errorf("writable absolute binds are forbidden")
			}
			volume.Source = relative
		}
		clean := filepath.Clean(volume.Source)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return Mount{}, fmt.Errorf("bind source escapes project")
		}
	} else {
		return Mount{}, fmt.Errorf("unsupported writable mount type %q", volume.Type)
	}
	base := filepath.Base(filepath.Clean(volume.Source))
	if base == "." || base == "" || base == string(filepath.Separator) {
		return Mount{}, fmt.Errorf("invalid mount source")
	}
	m := Mount{Dataset: "floatlab/" + stack + "/" + base, Source: volume.Source, Target: volume.Target, Type: volume.Type, RecordSize: "32K", Compression: "lz4"}
	if err := decodeString(volume.Extensions, "x-fl-recordsize", &m.RecordSize); err != nil {
		return Mount{}, err
	}
	if err := decodeString(volume.Extensions, "x-fl-compression", &m.Compression); err != nil {
		return Mount{}, err
	}
	if err := decodeString(volume.Extensions, "x-fl-quota", &m.Quota); err != nil {
		return Mount{}, err
	}
	if !validRecordSize(m.RecordSize) || !validCompression[strings.ToLower(m.Compression)] || (m.Quota != "" && m.Quota != "none" && !zfsSizePattern.MatchString(m.Quota)) {
		return Mount{}, fmt.Errorf("invalid ZFS properties")
	}
	var schedule string
	_ = decodeString(volume.Extensions, "x-fl-snapshots", &schedule)
	seen := map[string]bool{}
	for _, token := range strings.Fields(schedule) {
		match := snapshotPattern.FindStringSubmatch(token)
		if match == nil || seen[match[2]] {
			return Mount{}, fmt.Errorf("invalid or duplicate snapshot tier %q", token)
		}
		seen[match[2]] = true
		retain, _ := strconv.Atoi(match[3])
		m.Snapshots = append(m.Snapshots, SnapshotTier{Interval: match[1] + match[2], Retain: retain})
	}
	return m, nil
}

func validRecordSize(value string) bool {
	value = strings.ToUpper(value)
	for _, allowed := range []string{"4K", "8K", "16K", "32K", "64K", "128K", "256K", "512K", "1M"} {
		if value == allowed {
			return true
		}
	}
	return false
}

func validateAlerts(alerts []LifecycleAlert) error {
	seen := map[string]bool{}
	metrics := map[string]bool{"container_cpu_percent": true, "container_memory_percent": true, "container_restart_count": true, "managed_volume_used_percent": true, "managed_volume_free_bytes": true}
	comparators := map[string]bool{"gt": true, "gte": true, "lt": true, "lte": true, "eq": true}
	for _, alert := range alerts {
		if alert.Name == "" || seen[alert.Name] || !metrics[alert.Metric] || !comparators[alert.Comparator] {
			return fmt.Errorf("compose: invalid or duplicate alert rule %q", alert.Name)
		}
		seen[alert.Name] = true
		if duration, err := time.ParseDuration(alert.Duration); err != nil || duration <= 0 || alert.Severity == "" || alert.Message == "" {
			return fmt.Errorf("compose: invalid alert rule %q", alert.Name)
		}
	}
	return nil
}

func decodeExtension(value any, target any) error {
	if value == nil {
		return nil
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, target)
}

func decodeString(values map[string]any, key string, target *string) error {
	value, ok := values[key]
	if !ok {
		return nil
	}
	text, ok := value.(string)
	if !ok {
		return fmt.Errorf("%s must be a string", key)
	}
	*target = text
	return nil
}

func validateIPv4(value string) bool {
	return net.ParseIP(value) != nil && strings.Contains(value, ".")
}
