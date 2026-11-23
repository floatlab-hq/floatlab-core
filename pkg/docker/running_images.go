package docker

import (
	"context"
	"slices"

	"github.com/distribution/reference"
	composeapi "github.com/docker/compose/v5/pkg/api"
	composesdk "github.com/docker/compose/v5/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

// RunningImages verifies the saved runtime model against Docker's actual containers.
func (c *Client) RunningImages(ctx context.Context, projectName, path string) (ipc.ComposeRunningImages, error) {
	result := ipc.ComposeRunningImages{Matches: true, Images: map[string]ipc.RunningImage{}}
	project, err := c.compose.LoadProject(ctx, composeapi.ProjectLoadOptions{ConfigPaths: []string{path}, ProjectName: projectName})
	if err != nil {
		return result, err
	}
	list, err := c.dc.ContainerList(ctx, dockerclient.ContainerListOptions{All: true, Filters: make(dockerclient.Filters).Add("label", LabelComposeProject+"="+projectName)})
	if err != nil {
		return result, err
	}
	counts := map[string]int{}
	imageIDs := map[string]string{}
	for _, item := range list.Items {
		if item.Labels[composeapi.OneoffLabel] == "True" {
			continue
		}
		serviceName := item.Labels[composeapi.ServiceLabel]
		service, configured := project.Services[serviceName]
		if item.State != container.StateRunning {
			result.Matches = false
			continue
		}
		counts[serviceName]++
		if prior, ok := imageIDs[serviceName]; ok {
			result.Matches = result.Matches && prior == item.ImageID
		}
		imageIDs[serviceName] = item.ImageID
		inspect, err := c.dc.ContainerInspect(ctx, item.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			return result, err
		}
		actual := inspect.Container
		if actual.Config == nil || actual.State == nil || !actual.State.Running {
			result.Matches = false
			continue
		}
		if actual.State.Health != nil && actual.State.Health.Status != "healthy" {
			result.Matches = false
		}
		if _, ok := result.Images[serviceName]; !ok {
			image := ipc.RunningImage{Reference: actual.Config.Image}
			if ref, err := compose.ParseImageReference(image.Reference); err == nil {
				if ref.Digest != "" {
					image.Digests = []string{ref.Digest}
				} else if loaded, err := c.dc.ImageInspect(ctx, item.ImageID); err == nil {
					image.Digests = repositoryDigests(image.Reference, loaded.RepoDigests)
				}
			}
			result.Images[serviceName] = image
		}
		if configured && item.Labels[composeapi.ConfigHashLabel] != "" {
			// A save can pin an already deployed image without changing what runs.
			current, _ := compose.ParseImageReference(actual.Config.Image)
			if ref, err := compose.ParseImageReference(service.Image); err == nil && ref.Digest != "" && ref.CanonicalRepository == current.CanonicalRepository && slices.Contains(result.Images[serviceName].Digests, ref.Digest) {
				service.Image = actual.Config.Image
			}
			if service.Deploy != nil {
				deploy := *service.Deploy
				service.Deploy = &deploy
			}
			hash, err := composesdk.ServiceHash(service)
			result.Matches = result.Matches && err == nil && hash == item.Labels[composeapi.ConfigHashLabel]
		} else {
			result.Matches = false
		}
	}
	for serviceName, service := range project.Services {
		result.Matches = result.Matches && counts[serviceName] == service.GetScale()
	}
	return result, nil
}

func repositoryDigests(image string, candidates []string) []string {
	imageRef, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil
	}
	var digests []string
	for _, candidate := range candidates {
		digest, err := reference.ParseNormalizedNamed(candidate)
		if err != nil || digest.Name() != imageRef.Name() {
			continue
		}
		if canonical, ok := digest.(reference.Canonical); ok {
			digests = append(digests, canonical.Digest().String())
		}
	}
	slices.Sort(digests)
	return slices.Compact(digests)
}
