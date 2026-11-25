package main

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func put(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.WriteFile(path, value, 0600); err != nil {
		t.Fatal(err)
	}
}

func checkError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func imageArchive(t *testing.T, path, tag, architecture string) string {
	t.Helper()
	config := fmt.Appendf(nil, `{"architecture":%q,"os":"linux"}`, architecture)
	digest := sha256.Sum256(config)
	configPath := fmt.Sprintf("blobs/sha256/%x", digest)
	manifest := fmt.Appendf(nil, `[{"Config":%q,"RepoTags":[%q]}]`, configPath, tag)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(file)
	for _, entry := range []struct {
		name string
		data []byte
	}{{configPath, config}, {"manifest.json", manifest}} {
		if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: 0600, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("sha256:%x", digest)
}

func TestArchiveRequiresTagAndPlatform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "image.tar")
	imageArchive(t, path, "example:test", "amd64")
	_, err := savedImageID(path, "other:test")
	checkError(t, err, "tagged")
	imageArchive(t, path, "example:test", "arm64")
	_, err = savedImageID(path, "example:test")
	checkError(t, err, "linux/amd64")
}

func fixtureLock(t *testing.T) isoLock {
	t.Helper()
	lock, err := readJSON[isoLock](filepath.Join("../..", lockPath))
	if err != nil {
		t.Fatal(err)
	}
	return lock
}

func TestBundleAssemblyAndVerification(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "images"), 0700); err != nil {
		t.Fatal(err)
	}
	tool := newTool("../..")
	storeID := "sha256:" + strings.Repeat("a", 64)
	tool.run = func(args ...string) (string, error) {
		switch {
		case slices.Equal(args[:3], []string{"docker", "image", "inspect"}):
			return fmt.Sprintf(`[{"Id":%q,"Architecture":"amd64","Os":"linux"}]`, storeID), nil
		case args[1] == "save":
			imageArchive(t, args[3], args[4], "amd64")
		}
		return "", nil
	}
	if err := tool.containerBundle(directory, "floatlab-manage:dev", fixtureLock(t).Sidecars, storeID); err != nil {
		t.Fatal(err)
	}
	composePath := filepath.Join(directory, "docker-compose.yml")
	compose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "images.json")
	images, err := readJSON[[]bundleImage](path)
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range images {
		if !strings.Contains(string(compose), "image: "+image.Image) || image.ID == storeID {
			t.Fatalf("invalid bundled image: %+v", image)
		}
	}

	tool.run = func(args ...string) (string, error) {
		if !slices.Equal(args[:5], []string{"xorriso", "-osirrox", "on", "-indev", "/finished.iso"}) {
			t.Fatalf("unexpected xorriso arguments: %v", args)
		}
		for i := 5; i < len(args); i += 3 {
			if args[i] != "-extract" {
				t.Fatalf("unexpected xorriso arguments: %v", args)
			}
			source := filepath.Join(directory, strings.TrimPrefix(args[i+1], "/"))
			target := args[i+2]
			info, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsDir() {
				if err := os.CopyFS(target, os.DirFS(source)); err != nil {
					t.Fatal(err)
				}
			} else {
				content, err := os.ReadFile(source)
				if err != nil {
					t.Fatal(err)
				}
				put(t, target, content)
			}
		}
		return "", nil
	}
	if err := tool.verifyISO("/finished.iso"); err != nil {
		t.Fatal(err)
	}
	original := images[0].ID
	images[0].ID = storeID
	if err := writeJSON(path, images); err != nil {
		t.Fatal(err)
	}
	checkError(t, tool.verifyISO("/finished.iso"), "ID mismatch for floatlab-control.tar")
	images[0].ID = original
	if err := writeJSON(path, images[:3]); err != nil {
		t.Fatal(err)
	}
	checkError(t, verifyBundle(directory), "four core")
	if err := writeJSON(path, images); err != nil {
		t.Fatal(err)
	}
	put(t, composePath, []byte("services: {}\n"))
	checkError(t, verifyBundle(directory), "Compose does not reference")
	put(t, composePath, compose)

	for _, name := range []string{"floatlab-root.tar.gz", "zfsbootmenu.EFI"} {
		put(t, filepath.Join(directory, name), []byte("host"))
	}
	if err := finishBundle(directory, fixtureLock(t).Host.Manifest, "iso-test", "manage-test"); err != nil {
		t.Fatal(err)
	}
	manifest, err := readJSON[struct {
		hostManifest
		ISO    string `json:"iso_version"`
		Manage string `json:"manage_version"`
		Bytes  int64  `json:"bytes"`
	}](filepath.Join(directory, "floatlab-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ISO != "iso-test" || manifest.Manage != "manage-test" || len(manifest.Files) != 8 || manifest.NixpkgsRev == "" {
		t.Fatalf("unexpected bundle metadata: %+v", manifest)
	}
	var total int64
	for name, want := range manifest.Files {
		got, size, err := checksum(filepath.Join(directory, name))
		if err != nil || got != want {
			t.Fatalf("checksum for %s = %s (%v), want %s", name, got, err, want)
		}
		total += size
	}
	if manifest.Bytes != total {
		t.Fatalf("size = %d, want %d", manifest.Bytes, total)
	}
}

func TestLockValidation(t *testing.T) {
	tool := newTool("../..")
	if _, _, err := tool.validateLock(fixtureLock(t)); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*isoLock){
		func(lock *isoLock) { lock.Host = nil },
		func(lock *isoLock) { lock.Architecture = "arm64" },
		func(lock *isoLock) { lock.Host.Tag = "host-v99.0.0" },
		func(lock *isoLock) { lock.Sidecars["rqlite"] = "rqlite:latest" },
		func(lock *isoLock) {
			var host hostManifest
			_ = json.Unmarshal(lock.Host.Manifest, &host)
			host.Files["../escape"] = strings.Repeat("a", 64)
			lock.Host.Manifest, _ = json.Marshal(host)
		},
	} {
		lock := fixtureLock(t)
		mutate(&lock)
		if _, _, err := tool.validateLock(lock); err == nil {
			t.Fatal("accepted invalid lock")
		}
	}
}

func TestManagementRegistryMetadata(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"pkg/release", "releases"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(root, "pkg/release/compatibility.json"), []byte(`{"min_host_version":"0.2.0","max_host_version":"0.3.0"}`))
	for _, path := range []string{lockPath, "releases/rqlite-compatibility.json"} {
		data, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(root, path), data)
	}
	tool := newTool(root)
	path := t.TempDir()
	ghcr, hub := "ghcr.io/x:0.1.0", "docker.io/x:0.1.0"
	differentImages := false
	tool.run = func(args ...string) (string, error) {
		if args[1] == "buildx" {
			if args[4] == ghcr {
				return "sha256:" + strings.Repeat("a", 64), nil
			}
			return "sha256:" + strings.Repeat("b", 64), nil
		}
		if args[1] == "image" {
			id := "sha256:" + strings.Repeat("c", 64)
			if differentImages && strings.HasPrefix(args[3], "docker.io/") {
				id = "sha256:" + strings.Repeat("d", 64)
			}
			return fmt.Sprintf(`[{"Id":%q,"Architecture":"amd64","Os":"linux"}]`, id), nil
		}
		return "", nil
	}
	if err := tool.manageManifest("0.1.0", "commit", ghcr, hub, path); err != nil {
		t.Fatal(err)
	}
	manifest, err := readJSON[manageManifest](filepath.Join(path, "manage-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Registries["ghcr"].Digest == manifest.Registries["dockerhub"].Digest ||
		len(manifest.Images) != 4 || manifest.rqliteCompatibility.Version == "" || len(manifest.Sources) == 0 ||
		manifest.RequiredHost.Min != "0.2.0" || manifest.RequiredHost.Max != "0.3.0" {
		t.Fatalf("unexpected management metadata: %+v", manifest)
	}
	differentImages = true
	checkError(t, tool.manageManifest("0.1.0", "commit", ghcr, hub, t.TempDir()), "different management images")
}

func TestNotesAndWorkflowChecksums(t *testing.T) {
	tool := newTool("../..")
	notes, err := tool.notes("manage-v0.1.0")
	if err != nil || !strings.Contains(notes, "Publish") {
		t.Fatalf("notes = %q, error = %v", notes, err)
	}
	_, err = tool.notes("host-v99.0.0")
	checkError(t, err, "missing changelog entry")
	workflow, err := os.ReadFile(filepath.Join(tool.root, ".github/workflows/release-manage.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var command string
	for line := range strings.SplitSeq(string(workflow), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "sha256sum ") {
			command = line
		}
	}
	dir := t.TempDir()
	for _, name := range []string{"lab-linux-x64", "lab-linux-arm64", "lab-darwin-x64", "lab-darwin-arm64", "manage-manifest.json"} {
		put(t, filepath.Join(dir, name), []byte(name))
	}
	cmd := exec.Command("sh", "-ec", command+"\nsha256sum -c SHA256SUMS\ntest \"$(wc -l < SHA256SUMS)\" -eq 5")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workflow checksum command: %v\n%s", err, out)
	}
}

func TestISORejectsCorruptHostBeforePullingImages(t *testing.T) {
	tool := newTool("../..")
	lock := fixtureLock(t)
	tool.run = func(args ...string) (string, error) {
		if args[0] == "bash" {
			return "", nil
		}
		if args[0] != "gh" {
			t.Fatalf("ran %v before verifying host files", args)
		}
		path := filepath.Join(args[9], args[7])
		switch args[7] {
		case "host-manifest.json":
			put(t, path, lock.Host.Manifest)
		case "manage-manifest.json":
			put(t, path, lock.Manage.Manifest)
		default:
			put(t, path, []byte("corrupt host"))
		}
		return "", nil
	}
	checkError(t, tool.prepareISO(t.TempDir(), "0.1.0", ""), "checksum mismatch")
}

func TestLocalHostCopyVerifiesBeforeReplacing(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "source"), filepath.Join(dir, "target")
	put(t, source, []byte("host artifact"))
	put(t, target, []byte("existing artifact"))
	checkError(t, copyVerifiedHostFile(source, target, strings.Repeat("0", 64)), "checksum mismatch")
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "existing artifact" {
		t.Fatalf("failed copy changed existing artifact: %q, %v", got, err)
	}
	expected := fmt.Sprintf("%x", sha256.Sum256([]byte("host artifact")))
	if err := copyVerifiedHostFile(source, target, expected); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(target)
	if err != nil || string(got) != "host artifact" {
		t.Fatalf("verified copy got %q, %v", got, err)
	}
}
