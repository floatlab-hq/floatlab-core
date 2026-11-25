package main

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

type bundleImage struct {
	Archive string `json:"archive"`
	Image   string `json:"image"`
	ID      string `json:"id"`
	Source  string `json:"source"`
}

func archiveMember(path, name string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	archive := tar.NewReader(file)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: missing %s", path, name)
		}
		if err != nil {
			return nil, err
		}
		if header.Name == name {
			if header.Typeflag != tar.TypeReg || header.Size > 32<<20 {
				return nil, fmt.Errorf("%s: invalid metadata member %s", path, name)
			}
			return io.ReadAll(archive)
		}
	}
}

func savedImageID(path, image string) (string, error) {
	manifest, err := archiveMember(path, "manifest.json")
	if err != nil {
		return "", err
	}
	var images []struct {
		Config   string
		RepoTags []string
	}
	if err := json.Unmarshal(manifest, &images); err != nil {
		return "", err
	}
	if len(images) != 1 || !slices.Contains(images[0].RepoTags, image) {
		return "", fmt.Errorf("expected exactly one image tagged %s in %s", image, path)
	}
	config, err := archiveMember(path, images[0].Config)
	if err != nil {
		return "", err
	}
	var info struct{ Architecture, OS string }
	if err := json.Unmarshal(config, &info); err != nil {
		return "", err
	}
	if info.Architecture != "amd64" || info.OS != "linux" {
		return "", fmt.Errorf("expected linux/amd64 configuration in %s", path)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(config)), nil
}

func verifyBundle(directory string) error {
	images, err := readJSON[[]bundleImage](filepath.Join(directory, "images.json"))
	if err != nil {
		return err
	}
	expected := map[string]bool{"floatlab-control.tar": true}
	for _, name := range sidecarNames {
		expected[name+".tar"] = true
	}
	if len(images) != len(expected) {
		return errors.New("expected exactly the four core image archives")
	}
	for _, image := range images {
		if !expected[image.Archive] {
			return errors.New("expected exactly the four core image archives")
		}
		delete(expected, image.Archive)
	}
	compose, err := os.ReadFile(filepath.Join(directory, "docker-compose.yml"))
	if err != nil {
		return err
	}
	var stack struct {
		Services map[string]struct{ Image string }
	}
	if err := yaml.Unmarshal(compose, &stack); err != nil {
		return err
	}
	for _, image := range images {
		actual, err := savedImageID(filepath.Join(directory, "images", image.Archive), image.Image)
		if err != nil {
			return err
		}
		if image.ID != actual {
			return fmt.Errorf("bundled image ID mismatch for %s: metadata %s, saved configuration %s", image.Archive, image.ID, actual)
		}
		if stack.Services[strings.TrimSuffix(image.Archive, ".tar")].Image != image.Image {
			return fmt.Errorf("Compose does not reference bundled image %s", image.Image)
		}
	}
	return nil
}

func (tool releaseTool) verifyISO(path string) error {
	directory, err := os.MkdirTemp("", "floatlab-verify-iso-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	args := []string{"xorriso", "-osirrox", "on", "-indev", path}
	for _, name := range []string{"images", "images.json", "docker-compose.yml"} {
		args = append(args, "-extract", "/"+name, filepath.Join(directory, name))
	}
	if _, err := tool.run(args...); err != nil {
		return err
	}
	return verifyBundle(directory)
}

func (tool releaseTool) containerBundle(directory, management string, sidecars map[string]string, expectedManageID string) error {
	compose, err := os.ReadFile(filepath.Join(tool.root, "floatlab-appliance-image/files/core-stack/docker-compose.yml"))
	if err != nil {
		return err
	}
	var images []bundleImage
	services := append([]string{"floatlab-control"}, sidecarNames...)
	for _, service := range services {
		reference := sidecars[service]
		if service == "floatlab-control" {
			reference = management
		}
		if reference == "" {
			return fmt.Errorf("missing image reference for %s", service)
		}
		id, err := tool.imageID(reference)
		if err != nil {
			return err
		}
		if service == "floatlab-control" && expectedManageID != "" && id != expectedManageID {
			return errors.New("management image does not match release metadata")
		}
		// Use a tag that survives offline loading even if the saved image ID differs.
		local := "floatlab-bundle/" + service + ":" + strings.TrimPrefix(id, "sha256:")
		if _, err := tool.run("docker", "tag", reference, local); err != nil {
			return err
		}
		archive := service + ".tar"
		path := filepath.Join(directory, "images", archive)
		if _, err := tool.run("docker", "save", "--output", path, local); err != nil {
			return err
		}
		savedID, err := savedImageID(path, local)
		if err != nil {
			return err
		}
		images = append(images, bundleImage{archive, local, savedID, reference})
		pattern := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(service) + `:\n(?:    .*\n)*?    image: ([^\n]+)`)
		location := pattern.FindSubmatchIndex(compose)
		if location == nil {
			return fmt.Errorf("missing Compose image for %s", service)
		}
		compose = slices.Replace(compose, location[2], location[3], []byte(local)...)
	}
	if os.Getenv("FLOATLAB_INTEGRATION_COVERAGE") == "1" {
		compose = []byte(strings.ReplaceAll(string(compose), "      FLOATLAB_ACTIVATION_DIR:",
			"      GOCOVERDIR: /floatlab/system/integration-coverage\n      FLOATLAB_ACTIVATION_DIR:"))
	}
	if err := os.WriteFile(filepath.Join(directory, "docker-compose.yml"), compose, 0644); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(directory, "images.json"), images); err != nil {
		return err
	}
	return verifyBundle(directory)
}

func checksum(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return fmt.Sprintf("%x", hash.Sum(nil)), size, err
}

func copyVerifiedHostFile(source, destination, expected string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.CreateTemp(filepath.Dir(destination), ".host-artifact-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	digest := sha256.New()
	_, err = io.Copy(io.MultiWriter(output, digest), input)
	if err := errors.Join(err, output.Chmod(0644), output.Close()); err != nil {
		return err
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != expected {
		return fmt.Errorf("checksum mismatch: %s", filepath.Base(source))
	}
	return os.Rename(output.Name(), destination)
}

func manifestValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	err := decoder.Decode(&value)
	return value, err
}

func finishBundle(directory string, raw json.RawMessage, isoVersion, manageVersion string) error {
	var host map[string]json.RawMessage
	if err := json.Unmarshal(raw, &host); err != nil {
		return err
	}
	if host == nil {
		return errors.New("missing host manifest")
	}
	files := []string{"floatlab-root.tar.gz", "zfsbootmenu.EFI", "docker-compose.yml", "images.json"}
	for _, service := range append([]string{"floatlab-control"}, sidecarNames...) {
		files = append(files, "images/"+service+".tar")
	}
	digests := make(map[string]string, len(files))
	var total int64
	for _, name := range files {
		digest, size, err := checksum(filepath.Join(directory, name))
		if err != nil {
			return err
		}
		digests[name] = digest
		total += size
	}
	manifest := make(map[string]any, len(host)+4)
	for name, value := range host {
		manifest[name] = value
	}
	manifest["iso_version"] = isoVersion
	manifest["manage_version"] = manageVersion
	manifest["files"] = digests
	manifest["bytes"] = total
	return writeJSON(filepath.Join(directory, "floatlab-manifest.json"), manifest)
}

func (tool releaseTool) prepareISO(directory, isoVersion, localHost string) error {
	lock, err := readJSON[isoLock](filepath.Join(tool.root, lockPath))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(directory, "images"), 0755); err != nil {
		return err
	}
	if localHost != "" {
		localHost, err = filepath.Abs(localHost)
		if err != nil {
			return err
		}
		raw, err := readJSON[json.RawMessage](filepath.Join(localHost, "host-manifest.json"))
		if err != nil {
			return err
		}
		var host hostManifest
		if err := json.Unmarshal(raw, &host); err != nil {
			return err
		}
		if len(host.Files) != 2 {
			return errors.New("invalid local host artifact list")
		}
		for _, name := range []string{"floatlab-root.tar.gz", "zfsbootmenu.EFI"} {
			if !checksumPattern.MatchString(host.Files[name]) {
				return fmt.Errorf("invalid local host checksum for %s", name)
			}
			if err := copyVerifiedHostFile(filepath.Join(localHost, name), filepath.Join(directory, name), host.Files[name]); err != nil {
				return err
			}
		}
		const management = "floatlab-manage:dev"
		coverage := "0"
		if os.Getenv("FLOATLAB_INTEGRATION_COVERAGE") == "1" {
			coverage = "1"
		}
		if _, err := tool.run("docker", "build", "--build-arg", "INTEGRATION_COVERAGE="+coverage,
			"--tag", management, "--file", "docker/floatlab-control.Dockerfile", "."); err != nil {
			return err
		}
		if err := tool.containerBundle(directory, management, lock.Sidecars, ""); err != nil {
			return err
		}
		return finishBundle(directory, raw, isoVersion, "0.1.0-dev")
	}

	host, manage, err := tool.validateLock(lock)
	if err != nil {
		return err
	}
	for _, component := range []struct {
		name  string
		entry *releaseEntry
	}{{"host", lock.Host}, {"manage", lock.Manage}} {
		path, err := tool.download(lock.Repository, component.entry.Tag, component.name+"-manifest.json", directory)
		if err != nil {
			return err
		}
		raw, err := readJSON[json.RawMessage](path)
		if err != nil {
			return err
		}
		actual, err := manifestValue(raw)
		if err != nil {
			return err
		}
		expected, err := manifestValue(component.entry.Manifest)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(actual, expected) {
			return fmt.Errorf("published %s metadata differs from ISO lock", component.name)
		}
	}
	for name, expected := range host.Files {
		path, err := tool.download(lock.Repository, lock.Host.Tag, name, directory)
		if err != nil {
			return err
		}
		digest, _, err := checksum(path)
		if err != nil {
			return err
		}
		if digest != expected {
			return fmt.Errorf("checksum mismatch: %s", name)
		}
	}
	management := pinned(manage.Registries["ghcr"])
	if err := tool.containerBundle(directory, management, lock.Sidecars, manage.ImageID); err != nil {
		return err
	}
	var notes strings.Builder
	fmt.Fprintf(&notes, "Host %s; management %s.\n\n- management: `%s`\n", host.Version, manage.Version, management)
	for _, name := range sidecarNames {
		fmt.Fprintf(&notes, "- %s: `%s`\n", name, lock.Sidecars[name])
	}
	if err := os.WriteFile(filepath.Join(directory, "release-notes.md"), []byte(notes.String()), 0644); err != nil {
		return err
	}
	return finishBundle(directory, lock.Host.Manifest, isoVersion, manage.Version)
}
