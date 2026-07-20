package compose

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

func UpdateImages(source string, images map[string]string) (string, error) {
	if len(images) == 0 {
		return "", fmt.Errorf("compose: at least one image is required")
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		return "", fmt.Errorf("compose: parse image update: %w", err)
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
		imageNode := mappingValue(node, "image")
		if imageNode == nil {
			node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "image"}, &yaml.Node{Kind: yaml.ScalarNode, Value: image})
		} else {
			imageNode.Value = image
		}
	}
	out, err := yaml.Marshal(&document)
	if err != nil {
		return "", fmt.Errorf("compose: marshal image update: %w", err)
	}
	return string(out), nil
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
