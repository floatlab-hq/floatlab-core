package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/floatlab/floatlab-core/internal/credentials"
)

// Check before extracting as root; GNU tar also refuses writes through symlinks.
func validateRootArchive(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	links := map[string]bool{}
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(h.Name, "./")
		clean := filepath.Clean(name)
		if filepath.IsAbs(name) || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("unsafe root archive path %q", h.Name)
		}
		for parent := filepath.Dir(clean); parent != "."; parent = filepath.Dir(parent) {
			if links[parent] {
				return fmt.Errorf("root archive writes through link %q", parent)
			}
		}
		switch h.Typeflag {
		case tar.TypeReg, tar.TypeDir:
		case tar.TypeSymlink, tar.TypeLink:
			links[clean] = true
			target := h.Linkname
			if filepath.IsAbs(target) {
				// OS closures include runtime links such as /proc/mounts; never extract through them.
				if h.Typeflag != tar.TypeSymlink {
					return fmt.Errorf("unsafe root archive link %q", target)
				}
			} else {
				if h.Typeflag == tar.TypeSymlink {
					target = filepath.Join(filepath.Dir(clean), target)
				}
				if filepath.Clean(target) == ".." || strings.HasPrefix(filepath.Clean(target), "../") {
					return fmt.Errorf("unsafe root archive link %q", h.Linkname)
				}
			}
		default:
			return fmt.Errorf("unsupported root archive entry %q", h.Name)
		}
	}
}

func installOS(stopCtx context.Context, runtimeDir, mediaDir string, p Payload, a Appliance, manifest []byte, progress func(int)) (err error) {
	if err = a.validateIdentity(); err != nil {
		return err
	}
	ctx := context.WithoutCancel(stopCtx)
	step := func(name string, args ...string) error {
		if err := stopCtx.Err(); err != nil {
			return err
		}
		return run(ctx, name, args...)
	}
	version := "floatlab/os/" + p.Version
	base := version + "@base"
	image := runtimeDir + "/os"
	if _, e := Output(ctx, "zfs", "list", "-H", "-o", "name", "floatlab/os"); e != nil {
		if err = step("zfs", "create", "-o", "mountpoint=none", "-o", "canmount=off", "floatlab/os"); err != nil {
			return err
		}
	}
	if _, e := Output(ctx, "zfs", "list", "-H", "-t", "snapshot", "-o", "name", base); e != nil {
		if _, e = Output(ctx, "zfs", "list", "-H", "-o", "name", version); e == nil {
			owner, e := Output(ctx, "zfs", "get", "-H", "-o", "value", "org.floatlab:installer", version)
			if e != nil || strings.TrimSpace(string(owner)) != a.NodeID {
				return fmt.Errorf("OS dataset belongs to another installation")
			}
			// Only discard our incomplete dataset; never recursively destroy clones or snapshots.
			if err = step("zfs", "destroy", version); err != nil {
				return err
			}
		}
		if err = step("zfs", "create", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "org.zfsbootmenu:active=off", "-o", "org.floatlab:installer="+a.NodeID, version); err != nil {
			return err
		}
		if err = os.MkdirAll(image, 0700); err != nil {
			return err
		}
		if err = step("mount", "-t", "zfs", version, image); err != nil {
			return err
		}
		err = func() (err error) {
			defer func() { err = errors.Join(err, run(ctx, "umount", image)) }()
			if err = PopulateOSRoot(ctx, image, mediaDir+"/floatlab-root.tar.gz", p, a.HostID, manifest, progress); err != nil {
				return err
			}
			return step("zfs", "snapshot", base)
		}()
		if err != nil {
			return err
		}
	} else {
		if err = os.MkdirAll(image, 0700); err != nil {
			return err
		}
		if err = step("mount", "-t", "zfs", "-o", "ro", version, image); err != nil {
			return err
		}
		written, readErr := os.ReadFile(image + "/floatlab-manifest.json")
		id, idErr := os.ReadFile(image + "/floatlab-host-id")
		err = errors.Join(readErr, idErr, run(ctx, "umount", image))
		if err != nil {
			return err
		}
		if !bytes.Equal(written, manifest) || string(id) != a.HostID {
			return fmt.Errorf("existing OS snapshot does not match this installation")
		}
	}
	if err = step("zfs", "set", "readonly=on", version); err != nil {
		return err
	}
	if err = step("zfs", "set", "org.floatlab:host-id="+a.HostID, "org.floatlab:version="+p.Version, "org.floatlab:init="+p.Init, version); err != nil {
		return err
	}
	if _, e := Output(ctx, "zfs", "list", "-H", "-o", "name", "floatlab/os/current"); e != nil {
		if err = step("zfs", "clone", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "readonly=on", "-o", "org.zfsbootmenu:active=on", base, "floatlab/os/current"); err != nil {
			return err
		}
	} else {
		origin, e := Output(ctx, "zfs", "get", "-H", "-o", "value", "origin", "floatlab/os/current")
		if e != nil || strings.TrimSpace(string(origin)) != base {
			return fmt.Errorf("current OS belongs to another version; refusing replacement")
		}
	}
	if err = step("zfs", "set", "org.floatlab:host-id="+a.HostID, "org.floatlab:version="+p.Version, "org.floatlab:init="+p.Init, "org.floatlab:update=installer", "org.zfsbootmenu:commandline=init="+p.Init+" console=ttyS0,115200n8 spl.spl_hostid=0x"+a.HostID+" floatlab.installed=1", "org.zfsbootmenu:rootprefix=root=zfs:", "floatlab/os/current"); err != nil {
		return err
	}
	return step("zpool", "set", "bootfs=floatlab/os/current", "floatlab")
}

// PopulateOSRoot verifies and populates a mounted, empty version dataset without changing boot selection.
// Both installation and staged updates use the same archive and Nix-store trust boundary.
func PopulateOSRoot(ctx context.Context, image, archiveSource string, p Payload, hostID string, manifest []byte, progress func(int)) (err error) {
	archive := image + "/floatlab-root.tar.gz"
	if err = copyVerifiedProgress(archiveSource, archive, p.Files["floatlab-root.tar.gz"], progress); err != nil {
		return err
	}
	if err = validateRootArchive(archive); err != nil {
		return err
	}
	if err = run(ctx, "tar", "--extract", "--gzip", "--file="+archive, "--directory="+image, "--numeric-owner", "--same-owner", "--same-permissions"); err != nil {
		return err
	}
	if err = os.Remove(archive); err != nil {
		return err
	}
	for _, path := range []string{p.Init, "/boot/vmlinuz", "/boot/initramfs", "/nix-path-registration"} {
		if _, err = os.Stat(image + path); err != nil {
			return fmt.Errorf("incomplete installed root: %w", err)
		}
	}
	registration, err := os.Open(image + "/nix-path-registration")
	if err != nil {
		return err
	}
	defer registration.Close()
	if err = ctx.Err(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "nix-store", "--store", "local?root="+image, "--load-db")
	cmd.Stdin = registration
	if out, e := cmd.CombinedOutput(); e != nil {
		return fmt.Errorf("initialize installed Nix store: %w: %s", e, Diagnostic(out))
	}
	if err = credentials.WriteBytes(image+"/floatlab-manifest.json", manifest); err != nil {
		return err
	}
	if err = credentials.WriteBytes(image+"/floatlab-host-id", []byte(hostID)); err != nil {
		return err
	}
	return nil
}
