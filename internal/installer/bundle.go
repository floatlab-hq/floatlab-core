package installer

import (
	"fmt"
	"path/filepath"
	"strings"
)

func installContainerBundle(mediaDir, systemDir string, p Payload) error {
	for name, hash := range p.Files {
		if name == "floatlab-root.tar.gz" || name == "zfsbootmenu.EFI" {
			continue
		}
		if name != "docker-compose.yml" && name != "images.json" && !(strings.HasPrefix(name, "images/") && filepath.Ext(name) == ".tar" && filepath.Base(name) == strings.TrimPrefix(name, "images/")) {
			return fmt.Errorf("unexpected container bundle file %q", name)
		}
		if err := copyVerified(filepath.Join(mediaDir, name), filepath.Join(systemDir, name), hash); err != nil {
			return err
		}
	}
	return nil
}
