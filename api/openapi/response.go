package openapi

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"math"
	"strconv"
	"strings"
	"time"
)

func readDocument(file string) (map[string]interface{}, error) {
	data, err := specs.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document, nil
}
func resolveReference(file, ref string) (string, map[string]interface{}, error) {
	parts := strings.SplitN(ref, "#", 2)
	if len(parts) != 2 {
		return "", nil, fmt.Errorf("invalid reference %s", ref)
	}
	if parts[0] != "" {
		file = strings.TrimPrefix(parts[0], "./")
	}
	document, err := readDocument(file)
	if err != nil {
		return "", nil, err
	}
	var value interface{} = document
	for _, part := range strings.Split(strings.TrimPrefix(parts[1], "/"), "/") {
		object, ok := value.(map[string]interface{})
		if !ok {
			return "", nil, fmt.Errorf("invalid reference %s", ref)
		}
		value = object[strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")]
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return "", nil, fmt.Errorf("unresolved reference %s", ref)
	}
	return file, object, nil
}

// ValidateJSONResponse checks documented success statuses, JSON types, required
// fields, enum values and dates. It deliberately accepts legacy non-UUID node IDs.
func ValidateJSONResponse(method, path string, status int, body []byte) error {
	root, err := readDocument("openapi.yaml")
	if err != nil {
		return err
	}
	paths, _ := root["paths"].(map[string]interface{})
	item, ok := paths[strings.TrimPrefix(path, "/api/v1")].(map[string]interface{})
	if !ok {
		return fmt.Errorf("unknown contract path %s", path)
	}
	file := "openapi.yaml"
	if ref, ok := item["$ref"].(string); ok {
		file, item, err = resolveReference(file, ref)
		if err != nil {
			return err
		}
	}
	definition, ok := item[strings.ToLower(method)].(map[string]interface{})
	if !ok {
		return fmt.Errorf("unknown contract method %s %s", method, path)
	}
	responses, _ := definition["responses"].(map[string]interface{})
	response, ok := responses[strconv.Itoa(status)].(map[string]interface{})
	if !ok {
		return fmt.Errorf("undocumented response status %d for %s %s", status, method, path)
	}
	if status == 204 {
		if len(strings.TrimSpace(string(body))) > 0 {
			return fmt.Errorf("204 response must have no body")
		}
		return nil
	}
	content, _ := response["content"].(map[string]interface{})
	media, _ := content["application/json"].(map[string]interface{})
	schema, _ := media["schema"].(map[string]interface{})
	if schema == nil {
		return nil
	}
	var value interface{}
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	return validateValue(file, schema, value, "response")
}
func validateValue(file string, schema map[string]interface{}, value interface{}, location string) error {
	if value == nil {
		if nullable, _ := schema["nullable"].(bool); nullable {
			return nil
		}
	}
	if ref, ok := schema["$ref"].(string); ok {
		resolvedFile, resolved, err := resolveReference(file, ref)
		if err != nil {
			return err
		}
		return validateValue(resolvedFile, resolved, value, location)
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s must be an object", location)
		}
		required, _ := schema["required"].([]interface{})
		for _, key := range required {
			if _, ok := object[fmt.Sprint(key)]; !ok {
				return fmt.Errorf("%s missing %s", location, key)
			}
		}
		properties, _ := schema["properties"].(map[string]interface{})
		for key, property := range properties {
			if child, ok := object[key]; ok {
				if definition, ok := property.(map[string]interface{}); ok {
					if err := validateValue(file, definition, child, location+"."+key); err != nil {
						return err
					}
				}
			}
		}
	case "array":
		array, ok := value.([]interface{})
		if !ok {
			return fmt.Errorf("%s must be an array", location)
		}
		items, _ := schema["items"].(map[string]interface{})
		for i, child := range array {
			if items != nil {
				if err := validateValue(file, items, child, fmt.Sprintf("%s[%d]", location, i)); err != nil {
					return err
				}
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", location)
		}
		if schema["format"] == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
				return fmt.Errorf("%s must be RFC3339: %q", location, text)
			}
		}
	case "integer", "number":
		number, ok := value.(float64)
		if !ok || schema["type"] == "integer" && math.Trunc(number) != number {
			return fmt.Errorf("%s must be %s", location, schema["type"])
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", location)
		}
	}
	if values, ok := schema["enum"].([]interface{}); ok {
		for _, allowed := range values {
			if fmt.Sprint(allowed) == fmt.Sprint(value) {
				return nil
			}
		}
		return fmt.Errorf("%s has invalid enum value %v", location, value)
	}
	return nil
}
