package compose

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/distribution/reference"
)

type ImageReference struct {
	Repository          string
	CanonicalRepository string
	Tag                 string
	Digest              string
}

func ParseImageReference(image string) (ImageReference, error) {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return ImageReference{}, err
	}
	name, digest, _ := strings.Cut(image, "@")
	ref := ImageReference{Repository: name, CanonicalRepository: named.Name(), Digest: digest}
	if colon := strings.LastIndexByte(name, ':'); colon > strings.LastIndexByte(name, '/') {
		ref.Repository, ref.Tag = name[:colon], name[colon+1:]
	} else if digest == "" {
		ref.Tag = "latest"
	}
	return ref, nil
}

func (r ImageReference) WithDigest(digest string) string {
	image := r.Repository
	if r.Tag != "" {
		image += ":" + r.Tag
	}
	return image + "@" + digest
}

type ImageVersion struct {
	Numbers [3]uint64
	Parts   int
	Variant string
}

var imageVersionPattern = regexp.MustCompile(`^[vV]?([0-9]+)(?:\.([0-9]+))?(?:\.([0-9]+))?(?:-([a-zA-Z][a-zA-Z0-9._-]*))?$`)
var prereleaseVariant = regexp.MustCompile(`(?i)(^|[._-])(alpha|beta|rc|pre|preview|dev|snapshot|nightly|test|canary)[0-9]*($|[._-])`)

func ParseImageVersion(tag string) ImageVersion {
	parts := imageVersionPattern.FindStringSubmatch(tag)
	if parts == nil || prereleaseVariant.MatchString(parts[4]) {
		return ImageVersion{}
	}
	version := ImageVersion{Variant: parts[4]}
	for index, part := range parts[1:4] {
		if part == "" {
			break
		}
		number, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return ImageVersion{}
		}
		version.Numbers[index] = number
		version.Parts++
	}
	return version
}

func (v ImageVersion) Compare(other ImageVersion) int {
	for index, number := range v.Numbers {
		if number < other.Numbers[index] {
			return -1
		}
		if number > other.Numbers[index] {
			return 1
		}
	}
	return 0
}

func (v ImageVersion) Matches(other ImageVersion, parts int) bool {
	return v.Variant == other.Variant && v.Numbers[0] == other.Numbers[0] &&
		(parts < 2 || v.Numbers[1] == other.Numbers[1]) && (parts < 3 || v.Numbers[2] == other.Numbers[2])
}
