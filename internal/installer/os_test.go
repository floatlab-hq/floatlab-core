package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func rootArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, contents := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRootArchiveRejectsEscapesAndSpecialFiles(t *testing.T) {
	for _, h := range []tar.Header{
		{Name: "nix/store/example/init", Typeflag: tar.TypeReg},
		{Name: "init", Typeflag: tar.TypeSymlink, Linkname: "/nix/store/example/init"},
		{Name: "../escape", Typeflag: tar.TypeReg},
		{Name: "/etc/escape", Typeflag: tar.TypeReg},
		{Name: "boot/escape", Typeflag: tar.TypeSymlink, Linkname: "../../outside"},
		{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		{Name: "escape", Typeflag: tar.TypeLink, Linkname: "../outside"},
		{Name: "device", Typeflag: tar.TypeChar},
	} {
		t.Run(h.Name+string(h.Typeflag), func(t *testing.T) {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&h); err != nil {
				t.Fatal(err)
			}
			tw.Close()
			gz.Close()
			path := filepath.Join(t.TempDir(), "root.tar.gz")
			if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			err := validateRootArchive(path)
			wantValid := h.Name == "init" || h.Name == "nix/store/example/init" || h.Linkname == "/etc/passwd"
			if (err == nil) != wantValid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
}

func TestRootArchiveRejectsWritesThroughLinks(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, h := range []tar.Header{{Name: "outside", Typeflag: tar.TypeSymlink, Linkname: "/etc"}, {Name: "outside/passwd", Typeflag: tar.TypeReg}} {
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	path := filepath.Join(t.TempDir(), "root.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateRootArchive(path); err == nil {
		t.Fatal("archive can write outside root")
	}
}

func TestBuiltRootArchive(t *testing.T) {
	path := os.Getenv("FLOATLAB_ROOT_ARCHIVE")
	if path == "" {
		t.Skip("set FLOATLAB_ROOT_ARCHIVE to validate a built appliance root")
	}
	if err := validateRootArchive(path); err != nil {
		t.Fatal(err)
	}
}
