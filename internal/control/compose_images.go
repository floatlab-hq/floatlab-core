package control

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/ipc"
)

func (s *Server) lookupImage(ctx context.Context, node string, request ipc.ImageLookupPayload) (ipc.ImageLookupResult, error) {
	var result ipc.ImageLookupResult
	if s.hosts == nil {
		return result, fmt.Errorf("image lookup host is unavailable")
	}
	raw, err := s.hosts.Execute(ctx, node, "docker.image.lookup", request)
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}

func (s *Server) pinStackImages(ctx context.Context, node, source string, spec *compose.LifecycleSpec) (string, []string) {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	editable := compose.EditableImages(source)
	pins := map[string]string{}
	var warnings []string
	for name, service := range spec.Project.AllServices() {
		if service.Image == "" || service.Build != nil {
			continue
		}
		ref, err := compose.ParseImageReference(service.Image)
		if err != nil || ref.Digest != "" {
			continue
		}
		if !editable[name] {
			warnings = append(warnings, fmt.Sprintf("%s: image uses an indirect Compose value and could not be pinned", name))
			continue
		}
		result, err := s.lookupImage(ctx, node, ipc.ImageLookupPayload{Image: service.Image})
		if err != nil || result.Image == "" {
			warnings = append(warnings, fmt.Sprintf("%s: could not resolve %s in the registry", name, service.Image))
			continue
		}
		pins[name] = result.Image
		if result.Warning != "" {
			warnings = append(warnings, fmt.Sprintf("%s: %s", name, result.Warning))
		}
	}
	slices.Sort(warnings)
	if len(pins) == 0 {
		return source, warnings
	}
	updated, err := compose.PinImages(source, pins)
	if err != nil {
		return source, append(warnings, "could not save resolved image digests")
	}
	return updated, warnings
}
