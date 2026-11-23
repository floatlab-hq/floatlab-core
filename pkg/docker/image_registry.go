package docker

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// LookupImage resolves a save-time pin or finds the next image for a running service.
func LookupImage(ctx context.Context, request ipc.ImageLookupPayload) (ipc.ImageLookupResult, error) {
	switch request.Policy {
	case "", "disabled", "patch", "minor", "major":
	default:
		return ipc.ImageLookupResult{}, fmt.Errorf("unknown image upgrade policy %q", request.Policy)
	}
	ref, err := compose.ParseImageReference(request.Image)
	if err != nil {
		return ipc.ImageLookupResult{}, err
	}
	if ref.Tag == "" || (request.Policy == "" && ref.Digest != "") {
		return ipc.ImageLookupResult{}, nil
	}
	repo, err := name.NewRepository(ref.Repository)
	if err != nil {
		return ipc.ImageLookupResult{}, err
	}
	puller, err := remote.NewPuller(remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return ipc.ImageLookupResult{}, err
	}
	digestOf := func(tag string) (string, error) {
		descriptor, err := puller.Head(ctx, repo.Tag(tag))
		if err != nil {
			return "", err
		}
		return descriptor.Digest.String(), nil
	}
	version := compose.ParseImageVersion(ref.Tag)
	policy := request.Policy
	digests := request.Digests
	var currentDigest string
	if policy == "" || (version.Parts == 0 && ref.Tag != "latest") {
		currentDigest, err = digestOf(ref.Tag)
		if err != nil {
			return ipc.ImageLookupResult{}, err
		}
	}
	if policy == "" {
		digests = []string{currentDigest}
	}
	if len(digests) == 0 {
		return ipc.ImageLookupResult{}, nil
	}
	if ref.Tag != "latest" && version.Parts == 0 || policy == "" && version.Parts == 3 {
		if policy != "" && slices.Contains(digests, currentDigest) {
			return ipc.ImageLookupResult{}, nil
		}
		return ipc.ImageLookupResult{Image: ref.WithDigest(currentDigest)}, nil
	}
	if policy == "" {
		// shortcut: bound alias lookup to five seconds, cache metadata if large repositories routinely exceed it.
		aliasCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		ctx = aliasCtx
	}
	tags, err := puller.List(ctx, repo)
	if err != nil {
		if policy == "" {
			return ipc.ImageLookupResult{Image: ref.WithDigest(currentDigest), Warning: "could not look up equivalent version tags"}, nil
		}
		return ipc.ImageLookupResult{}, err
	}
	candidates := stableImageTags(tags)
	if version.Parts < 3 {
		for _, tag := range candidates {
			if version.Parts > 0 && !version.Matches(tag.version, version.Parts) {
				continue
			}
			digest, err := digestOf(tag.name)
			if err != nil {
				if policy == "" {
					return ipc.ImageLookupResult{Image: ref.WithDigest(currentDigest), Warning: "could not look up equivalent version tags"}, nil
				}
				return ipc.ImageLookupResult{}, err
			}
			if slices.Contains(digests, digest) {
				ref.Tag, version = tag.name, tag.version
				break
			}
		}
	}
	if policy == "" {
		result := ipc.ImageLookupResult{Image: ref.WithDigest(currentDigest)}
		if version.Parts < 3 {
			result.Warning = "no equivalent full version tag was found"
		}
		return result, nil
	}
	if version.Parts < 3 {
		currentDigest, err = digestOf(ref.Tag)
		if err != nil || slices.Contains(digests, currentDigest) {
			return ipc.ImageLookupResult{}, err
		}
		return ipc.ImageLookupResult{Image: ref.WithDigest(currentDigest)}, nil
	}
	for _, tag := range candidates {
		if !canUpgradeImage(version, tag.version, policy) {
			continue
		}
		digest, err := digestOf(tag.name)
		if err != nil {
			return ipc.ImageLookupResult{}, err
		}
		if !slices.Contains(digests, digest) {
			ref.Tag = tag.name
			return ipc.ImageLookupResult{Image: ref.WithDigest(digest)}, nil
		}
	}
	return ipc.ImageLookupResult{}, nil
}

type imageTag struct {
	name    string
	version compose.ImageVersion
}

func stableImageTags(tags []string) []imageTag {
	var versions []imageTag
	for _, tag := range tags {
		if version := compose.ParseImageVersion(tag); version.Parts == 3 {
			versions = append(versions, imageTag{tag, version})
		}
	}
	sort.Slice(versions, func(i, j int) bool {
		a, b := versions[i], versions[j]
		if order := a.version.Compare(b.version); order != 0 {
			return order > 0
		}
		if len(a.name) != len(b.name) {
			return len(a.name) < len(b.name)
		}
		return a.name < b.name
	})
	return versions
}

func canUpgradeImage(current, candidate compose.ImageVersion, policy string) bool {
	if current.Variant != candidate.Variant || candidate.Compare(current) <= 0 {
		return false
	}
	switch policy {
	case "patch":
		return current.Matches(candidate, 2)
	case "minor":
		return current.Matches(candidate, 1)
	case "major", "disabled":
		return true
	default:
		return false
	}
}
