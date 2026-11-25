// Command release assembles and verifies FloatLab release artifacts.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/spf13/pflag"
)

const lockPath = "releases/iso.lock.json"

var (
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	checksumPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	revisionPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	sidecarNames      = []string{"rqlite", "victoria-metrics", "victoria-logs"}
)

type registry struct {
	Reference string `json:"reference"`
	Digest    string `json:"digest"`
}

type releaseEntry struct {
	Tag      string          `json:"tag"`
	Manifest json.RawMessage `json:"manifest"`
}

type isoLock struct {
	Architecture string            `json:"architecture"`
	Repository   string            `json:"repository"`
	Host         *releaseEntry     `json:"host"`
	Manage       *releaseEntry     `json:"manage"`
	Sidecars     map[string]string `json:"sidecars"`
}

type hostManifest struct {
	Version      string            `json:"version"`
	Architecture string            `json:"architecture"`
	NixpkgsRev   string            `json:"nixpkgs_rev"`
	Files        map[string]string `json:"files"`
}

type rqliteCompatibility struct {
	Version string   `json:"rqlite_version"`
	Sources []string `json:"rqlite_sources"`
}

type manageManifest struct {
	Version      string              `json:"version"`
	Commit       string              `json:"git_commit"`
	Architecture string              `json:"architecture"`
	RequiredHost release.HostRange   `json:"required_host"`
	ImageID      string              `json:"image_id"`
	Registries   map[string]registry `json:"registries"`
	rqliteCompatibility
	Images map[string]string `json:"images"`
}

type releaseTool struct {
	root string
	run  func(...string) (string, error)
}

func newTool(root string) releaseTool {
	return releaseTool{root, func(args ...string) (string, error) {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = root
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("%s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(string(out)), nil
	}}
}

func readJSON[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, fmt.Errorf("%s: %w", path, err)
	}
	return value, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0644)
}

func (tool releaseTool) notes(tag string) (string, error) {
	if _, err := tool.run("bash", "scripts/check-release-tag.sh", tag); err != nil {
		return "", err
	}
	component, version, _ := strings.Cut(tag, "-v")
	data, err := os.ReadFile(filepath.Join(tool.root, "CHANGELOG-"+component+".md"))
	if err != nil {
		return "", err
	}
	var lines []string
	found := false
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, "## ") {
			if found {
				break
			}
			found = strings.TrimSpace(line) == "## "+version
			continue
		}
		if found {
			lines = append(lines, line)
		}
	}
	if notes := strings.TrimSpace(strings.Join(lines, "\n")); notes != "" {
		return notes + "\n", nil
	}
	return "", fmt.Errorf("missing changelog entry for %s", tag)
}

func (tool releaseTool) download(repository, tag, name, directory string) (string, error) {
	_, err := tool.run("gh", "release", "download", tag, "--repo", repository,
		"--pattern", name, "--dir", directory, "--clobber")
	return filepath.Join(directory, name), err
}

func (tool releaseTool) registryDigest(reference string) (string, error) {
	digest, err := tool.run("docker", "buildx", "imagetools", "inspect", reference, "--format", "{{.Manifest.Digest}}")
	if err != nil {
		return "", err
	}
	if !digestPattern.MatchString(digest) {
		return "", fmt.Errorf("invalid registry digest for %s: %s", reference, digest)
	}
	return digest, nil
}

func (tool releaseTool) imageID(reference string) (string, error) {
	if reference != "floatlab-manage:dev" {
		if _, err := tool.run("docker", "pull", "--platform", "linux/amd64", reference); err != nil {
			return "", err
		}
	}
	raw, err := tool.run("docker", "image", "inspect", reference)
	if err != nil {
		return "", err
	}
	var info []struct{ ID, Architecture, OS string }
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		return "", err
	}
	if len(info) != 1 || info[0].Architecture != "amd64" || info[0].OS != "linux" {
		return "", fmt.Errorf("expected linux/amd64: %s", reference)
	}
	if !digestPattern.MatchString(info[0].ID) {
		return "", fmt.Errorf("invalid image ID for %s: %s", reference, info[0].ID)
	}
	return info[0].ID, nil
}

func pinned(image registry) string {
	reference := image.Reference
	if index := strings.LastIndexByte(reference, ':'); index > strings.LastIndexByte(reference, '/') {
		reference = reference[:index]
	}
	return reference + "@" + image.Digest
}

func (tool releaseTool) pinSidecars(sidecars map[string]string) error {
	for _, name := range sidecarNames {
		reference := sidecars[name]
		if reference == "" {
			return fmt.Errorf("missing sidecar %s", name)
		}
		if !strings.Contains(reference, "@") {
			digest, err := tool.registryDigest(reference)
			if err != nil {
				return err
			}
			sidecars[name] = reference + "@" + digest
		}
		if _, digest, _ := strings.Cut(sidecars[name], "@"); !digestPattern.MatchString(digest) {
			return fmt.Errorf("unpinned sidecar: %s", reference)
		}
	}
	return nil
}

func (tool releaseTool) manageManifest(version, commit, ghcr, dockerhub, directory string) error {
	if _, err := release.Parse(version); err != nil {
		return err
	}
	requiredHost, err := readJSON[release.HostRange](filepath.Join(tool.root, "pkg/release/compatibility.json"))
	if err != nil {
		return err
	}
	if err := requiredHost.Validate(); err != nil {
		return err
	}
	value := manageManifest{Version: version, Commit: commit, Architecture: "amd64",
		RequiredHost: requiredHost, Registries: make(map[string]registry)}
	for _, image := range []struct{ name, reference string }{{"ghcr", ghcr}, {"dockerhub", dockerhub}} {
		digest, err := tool.registryDigest(image.reference)
		if err != nil {
			return err
		}
		entry := registry{image.reference, digest}
		id, err := tool.imageID(pinned(entry))
		if err != nil {
			return err
		}
		if value.ImageID != "" && id != value.ImageID {
			return errors.New("registries contain different management images")
		}
		value.ImageID = id
		value.Registries[image.name] = entry
	}
	compatibility, err := readJSON[rqliteCompatibility](filepath.Join(tool.root, "releases/rqlite-compatibility.json"))
	if err != nil {
		return err
	}
	if len(compatibility.Sources) == 0 {
		return errors.New("explicit tested rqlite versions required")
	}
	for _, version := range append([]string{compatibility.Version}, compatibility.Sources...) {
		if _, err := release.Parse(version); err != nil {
			return err
		}
	}
	lock, err := readJSON[isoLock](filepath.Join(tool.root, lockPath))
	if err != nil {
		return err
	}
	if err := tool.pinSidecars(lock.Sidecars); err != nil {
		return err
	}
	rqliteRef, _, _ := strings.Cut(lock.Sidecars["rqlite"], "@")
	if !strings.HasSuffix(rqliteRef, ":"+compatibility.Version) {
		return errors.New("rqlite compatibility policy does not match target image tag")
	}
	value.rqliteCompatibility = compatibility
	value.Images = lock.Sidecars
	value.Images["floatlab-control"] = pinned(value.Registries["ghcr"])
	return writeJSON(filepath.Join(directory, "manage-manifest.json"), value)
}

func (tool releaseTool) validateLock(lock isoLock) (hostManifest, manageManifest, error) {
	var host hostManifest
	var manage manageManifest
	err := func() error {
		if lock.Architecture != "amd64" {
			return errors.New("only amd64 ISO releases are supported")
		}
		if !repositoryPattern.MatchString(lock.Repository) {
			return errors.New("invalid GitHub repository")
		}
		if lock.Host == nil || lock.Manage == nil {
			return errors.New("release host/manage first, then run release lock")
		}
		if err := json.Unmarshal(lock.Host.Manifest, &host); err != nil {
			return err
		}
		if err := json.Unmarshal(lock.Manage.Manifest, &manage); err != nil {
			return err
		}
		for _, entry := range []struct{ tag, expected, architecture string }{
			{lock.Host.Tag, "host-v" + host.Version, host.Architecture},
			{lock.Manage.Tag, "manage-v" + manage.Version, manage.Architecture},
		} {
			if entry.tag != entry.expected || entry.architecture != "amd64" {
				return fmt.Errorf("invalid release tag or architecture: %s", entry.tag)
			}
			if _, err := tool.run("bash", "scripts/check-release-tag.sh", entry.tag); err != nil {
				return err
			}
		}
		if len(host.Files) != 2 || !checksumPattern.MatchString(host.Files["floatlab-root.tar.gz"]) ||
			!checksumPattern.MatchString(host.Files["zfsbootmenu.EFI"]) {
			return errors.New("invalid host artifact list or checksum")
		}
		if !revisionPattern.MatchString(host.NixpkgsRev) {
			return errors.New("host must pin its Nixpkgs revision")
		}
		ok, err := manage.RequiredHost.Contains(host.Version)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("host %s is outside >=%s <%s", host.Version, manage.RequiredHost.Min, manage.RequiredHost.Max)
		}
		ghcr := manage.Registries["ghcr"]
		if !strings.HasPrefix(ghcr.Reference, "ghcr.io/") || !digestPattern.MatchString(ghcr.Digest) {
			return errors.New("management must pin a GHCR image digest")
		}
		if len(lock.Sidecars) != len(sidecarNames) {
			return errors.New("expected rqlite, metrics and logs sidecars")
		}
		for _, name := range sidecarNames {
			ref, digest, _ := strings.Cut(lock.Sidecars[name], "@")
			if ref == "" || !digestPattern.MatchString(digest) {
				return fmt.Errorf("unpinned sidecar: %s", name)
			}
		}
		return nil
	}()
	return host, manage, err
}

func (tool releaseTool) updateLock(lock isoLock, hostTag, manageTag string) error {
	dir, err := os.MkdirTemp("", "floatlab-release-lock-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if !repositoryPattern.MatchString(lock.Repository) {
		return errors.New("invalid GitHub repository")
	}
	for _, component := range []struct {
		name, tag string
		target    **releaseEntry
	}{
		{"host", hostTag, &lock.Host}, {"manage", manageTag, &lock.Manage},
	} {
		if !strings.HasPrefix(component.tag, component.name+"-v") {
			return fmt.Errorf("invalid %s tag %q", component.name, component.tag)
		}
		if _, err := tool.run("bash", "scripts/check-release-tag.sh", component.tag); err != nil {
			return err
		}
		path, err := tool.download(lock.Repository, component.tag, component.name+"-manifest.json", dir)
		if err != nil {
			return err
		}
		raw, err := readJSON[json.RawMessage](path)
		if err != nil {
			return err
		}
		*component.target = &releaseEntry{Tag: component.tag, Manifest: raw}
	}
	if err := tool.pinSidecars(lock.Sidecars); err != nil {
		return err
	}
	if _, _, err := tool.validateLock(lock); err != nil {
		return err
	}
	// Replace the lock only after the entire candidate has been verified and written.
	path := filepath.Join(tool.root, lockPath)
	file, err := os.CreateTemp(filepath.Dir(path), ".iso-lock-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	err = errors.Join(encoder.Encode(lock), file.Chmod(0644), file.Close())
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (tool releaseTool) command(args []string) error {
	const usage = "usage: release {notes|lock|manage-manifest|prepare-iso|bundle-revision|validate-lock|verify-iso} [arguments]"
	if len(args) == 0 {
		return errors.New(usage)
	}
	if args[0] == "-h" || args[0] == "--help" {
		_, err := fmt.Println(usage)
		return err
	}
	switch args[0] {
	case "notes":
		if len(args) == 2 {
			notes, err := tool.notes(args[1])
			if err == nil {
				_, err = fmt.Print(notes)
			}
			return err
		}
	case "lock":
		lock, err := readJSON[isoLock](filepath.Join(tool.root, lockPath))
		if err != nil {
			return err
		}
		flags := pflag.NewFlagSet("lock", pflag.ContinueOnError)
		flags.StringVar(&lock.Repository, "repository", lock.Repository, "GitHub repository")
		host := flags.String("host", "", "host release tag")
		manage := flags.String("manage", "", "management release tag")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() == 0 && *host != "" && *manage != "" {
			return tool.updateLock(lock, *host, *manage)
		}
	case "manage-manifest":
		if len(args) == 6 {
			return tool.manageManifest(args[1], args[2], args[3], args[4], args[5])
		}
	case "prepare-iso":
		flags := pflag.NewFlagSet("prepare-iso", pflag.ContinueOnError)
		version := flags.String("version", "", "ISO version")
		host := flags.String("local-host", "", "local host artifact directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() == 1 && *version != "" {
			dir, err := filepath.Abs(flags.Arg(0))
			if err != nil {
				return err
			}
			return tool.prepareISO(dir, *version, *host)
		}
	case "bundle-revision":
		if len(args) == 2 {
			manifest, err := readJSON[hostManifest](filepath.Join(args[1], "floatlab-manifest.json"))
			if err != nil {
				return err
			}
			if !revisionPattern.MatchString(manifest.NixpkgsRev) {
				return errors.New("bundle must pin its Nixpkgs revision")
			}
			_, err = fmt.Println(manifest.NixpkgsRev)
			return err
		}
	case "validate-lock":
		if len(args) == 1 {
			lock, err := readJSON[isoLock](filepath.Join(tool.root, lockPath))
			if err != nil {
				return err
			}
			_, _, err = tool.validateLock(lock)
			return err
		}
	case "verify-iso":
		if len(args) == 2 {
			path, err := filepath.Abs(args[1])
			if err != nil {
				return err
			}
			return tool.verifyISO(path)
		}
	}
	return fmt.Errorf("invalid arguments for release %s", args[0])
}

func main() {
	root, err := os.Getwd()
	if err == nil {
		for {
			if _, err = os.Stat(filepath.Join(root, lockPath)); err == nil {
				break
			}
			parent := filepath.Dir(root)
			if parent == root {
				err = errors.New("run release from the FloatLab checkout")
				break
			}
			root = parent
		}
	}
	if err == nil {
		err = newTool(root).command(os.Args[1:])
	}
	if errors.Is(err, pflag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
