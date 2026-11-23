package compose

import (
	"fmt"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"gopkg.in/yaml.v3"
)

const (
	UpgradePolicyKey = "x-fl-auto-upgrade"
	ManagedImageKey  = "x-fl-managed-image"
)

func validUpgradePolicy(value string) bool {
	switch value {
	case "disabled", "patch", "minor", "major":
		return true
	default:
		return false
	}
}

func UpgradePolicy(service types.ServiceConfig) string {
	if policy, _ := service.Extensions[UpgradePolicyKey].(string); policy != "" {
		return policy
	}
	return "patch"
}

func ManagedImage(service types.ServiceConfig) bool {
	return service.Image != "" && service.Extensions[ManagedImageKey] == service.Image
}

// EditableImages identifies image fields that can be pinned without changing
// shared YAML anchors or replacing Compose interpolation with today's value.
func EditableImages(source string) map[string]bool {
	var document yaml.Node
	if yaml.Unmarshal([]byte(source), &document) != nil || len(document.Content) == 0 {
		return nil
	}
	services := mappingValue(document.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return nil
	}
	editable := map[string]bool{}
	for index := 0; index+1 < len(services.Content); index += 2 {
		service := services.Content[index+1]
		image := mappingValue(service, "image")
		direct := image == nil || image.Kind == yaml.ScalarNode && image.Anchor == "" && !strings.Contains(image.Value, "$")
		alias := image != nil && image.Kind == yaml.AliasNode && image.Alias != nil && image.Alias.Kind == yaml.ScalarNode && !strings.Contains(image.Alias.Value, "$")
		editable[services.Content[index].Value] = service.Kind == yaml.MappingNode && (direct || alias)
	}
	return editable
}

func UpdateImages(source string, images map[string]string) (string, error) {
	return writeImages(source, images, false)
}

func PinImages(source string, images map[string]string) (string, error) {
	return writeImages(source, images, true)
}

func writeImages(source string, images map[string]string, managed bool) (string, error) {
	if len(images) == 0 {
		return "", fmt.Errorf("compose: at least one image is required")
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", fmt.Errorf("compose: parse image update: %w", err)
	}
	if len(document.Content) == 0 {
		return "", fmt.Errorf("compose: services mapping is required")
	}
	services := mappingValue(document.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", fmt.Errorf("compose: services mapping is required")
	}
	for service, image := range images {
		if image == "" {
			return "", fmt.Errorf("compose: image for service %s is empty", service)
		}
		node := mappingValue(services, service)
		if node == nil || node.Kind != yaml.MappingNode {
			return "", fmt.Errorf("compose: unknown service %s", service)
		}
		setString(node, "image", image)
		if managed {
			setString(node, ManagedImageKey, image)
		}
	}
	out, err := yaml.Marshal(&document)
	if err != nil {
		return "", fmt.Errorf("compose: marshal image update: %w", err)
	}
	return string(out), nil
}

func setString(node *yaml.Node, key, value string) {
	if field := mappingValue(node, key); field != nil {
		field.Kind, field.Tag, field.Value, field.Alias = yaml.ScalarNode, "!!str", value, nil
		return
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Value: value})
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}
