package compose

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var datasetComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)

// ProjectSourcePath gives the Compose path at a managed dataset's mountpoint.
func ProjectSourcePath(dataset string) (string, error) {
	for _, part := range strings.Split(dataset, "/") {
		if !datasetComponent.MatchString(part) {
			return "", fmt.Errorf("invalid dataset path %q", dataset)
		}
	}
	return filepath.Join("/", dataset, "docker-compose.yml"), nil
}

// ReadProjectSource reads the project file from an active dataset or snapshot.
func ReadProjectSource(path string) (string, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	defer root.Close()
	name := filepath.Base(path)
	info, err := root.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("Compose source is not a regular file: %s", path)
	}
	content, err := root.ReadFile(name)
	return string(content), err
}
