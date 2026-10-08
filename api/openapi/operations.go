package openapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Operation identifies an HTTP contract after resolving management path references.
type Operation struct {
	Method          string `json:"method"`
	Path            string `json:"path"`
	ID              string `json:"operation_id"`
	SuccessStatuses []int  `json:"success_statuses"`
	Public          bool   `json:"public"`
}

func ManagementOperations() ([]Operation, error) {
	data, err := specs.ReadFile("openapi.yaml")
	if err != nil {
		return nil, err
	}
	var root struct {
		Paths map[string]map[string]interface{} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	operations := []Operation{}
	for path, item := range root.Paths {
		if ref, ok := item["$ref"].(string); ok {
			parts := strings.SplitN(ref, "#", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("invalid path reference %s", ref)
			}
			data, err := specs.ReadFile(strings.TrimPrefix(parts[0], "./"))
			if err != nil {
				return nil, err
			}
			var document map[string]interface{}
			if err := yaml.Unmarshal(data, &document); err != nil {
				return nil, err
			}
			var current interface{} = document
			for _, segment := range strings.Split(strings.TrimPrefix(parts[1], "/"), "/") {
				segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
				object, ok := current.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("invalid path reference %s", ref)
				}
				current = object[segment]
			}
			var ok bool
			item, ok = current.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("unresolved path reference %s", ref)
			}
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head", "options"} {
			value, exists := item[method]
			if !exists {
				continue
			}
			definition, ok := value.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid operation %s %s", method, path)
			}
			id, _ := definition["operationId"].(string)
			if id == "" {
				return nil, fmt.Errorf("missing operationId: %s %s", method, path)
			}
			public := false
			if security, ok := definition["security"].([]interface{}); ok && len(security) == 0 {
				public = true
			}
			statuses := []int{}
			if responses, ok := definition["responses"].(map[string]interface{}); ok {
				for code := range responses {
					status, err := strconv.Atoi(code)
					if err == nil && status >= 200 && status < 300 {
						statuses = append(statuses, status)
					}
				}
			}
			sort.Ints(statuses)
			operations = append(operations, Operation{SuccessStatuses: statuses, Method: strings.ToUpper(method), Path: "/api/v1" + path, ID: id, Public: public})
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].Path+operations[i].Method < operations[j].Path+operations[j].Method
	})
	return operations, nil
}
