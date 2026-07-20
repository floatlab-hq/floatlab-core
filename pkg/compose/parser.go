package compose

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
)

const (
	stackExtKey  = "x-fl-stack"
	volumeExtKey = "x-fl-volumes"
)

// Parse loads and validates a FloatLab-annotated docker-compose YAML.
// projectName is used as a fallback when the compose file has no "name:" key.
func Parse(ctx context.Context, yamlContent, projectName string) (*ParsedStack, error) {
	project, err := loadProjectAt(yamlContent, projectName, "/")
	if err != nil {
		return nil, err
	}

	ext, err := extractStackExt(project)
	if err != nil {
		return nil, err
	}

	name := project.Name
	if name == "" {
		name = projectName
	}

	return &ParsedStack{
		Extension:      ext,
		ServiceVolumes: extractServiceVolumes(project),
		ProjectName:    name,
	}, nil
}

func loadProjectAt(yamlContent, projectName, workingDir string) (*types.Project, error) {
	project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		ConfigFiles: []types.ConfigFile{
			{Content: []byte(yamlContent)},
		},
		WorkingDir:  workingDir,
		Environment: map[string]string{},
	}, func(o *loader.Options) {
		o.SkipNormalization = true
		o.SetProjectName(projectName, false)
	})
	if err != nil {
		return nil, fmt.Errorf("compose: parse: %w", err)
	}
	return project, nil
}

// ParseAndValidate is a convenience wrapper that parses then validates.
func ParseAndValidate(ctx context.Context, yamlContent, projectName string) (*ParsedStack, error) {
	ps, err := Parse(ctx, yamlContent, projectName)
	if err != nil {
		return nil, err
	}
	if err := Validate(ps); err != nil {
		return nil, err
	}
	return ps, nil
}

func extractStackExt(p *types.Project) (StackExtension, error) {
	raw, ok := p.Extensions[stackExtKey]
	if !ok {
		return StackExtension{}, fmt.Errorf("compose: missing %s extension", stackExtKey)
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return StackExtension{}, fmt.Errorf("compose: marshal %s: %w", stackExtKey, err)
	}
	var ext StackExtension
	if err := json.Unmarshal(b, &ext); err != nil {
		return StackExtension{}, fmt.Errorf("compose: decode %s: %w", stackExtKey, err)
	}
	return ext, nil
}

func extractServiceVolumes(p *types.Project) map[string]map[string]VolumeExtension {
	out := make(map[string]map[string]VolumeExtension)
	for name, svc := range p.Services {
		raw, ok := svc.Extensions[volumeExtKey]
		if !ok {
			continue
		}
		b, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		var vols map[string]VolumeExtension
		if err := json.Unmarshal(b, &vols); err != nil {
			continue
		}
		out[name] = vols
	}
	return out
}
