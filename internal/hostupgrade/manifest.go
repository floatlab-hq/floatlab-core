package hostupgrade

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/internal/installer"
	"github.com/floatlab/floatlab-core/pkg/release"
)

const RecoveryProtocol = 1
const Directory = "/floatlab/system/updates"
const Repository = "floatlab-hq/floatlab-core"

type Engine struct {
	Docker     string `json:"docker"`
	Containerd string `json:"containerd"`
	Driver     string `json:"storage_driver"`
	DataRoot   string `json:"data_root"`
}

type Manifest struct {
	installer.Payload
	Architecture      string `json:"architecture"`
	RecoveryProtocol  int    `json:"recovery_protocol"`
	Bootloader        string `json:"bootloader"`
	PoolCompatibility string `json:"pool_compatibility"`
	Engine            Engine `json:"engine"`
	// Only release qualification may add a source; empty means staging only.
	QualifiedSources []string `json:"qualified_sources"`
	InstalledHostID  string   `json:"installed_host_id,omitempty"`
}

func (m Manifest) Validate() error {
	if err := m.Payload.Validate(); err != nil {
		return err
	}
	if _, err := release.Parse(m.Version); err != nil {
		return err
	}
	if m.Architecture != "amd64" || m.RecoveryProtocol != RecoveryProtocol || m.Bootloader == "" || m.PoolCompatibility != "openzfs-2.3-linux" {
		return fmt.Errorf("unsupported host release metadata or recovery protocol")
	}
	if m.RootBytes > 1<<40 || m.Bytes > 1<<40 {
		return fmt.Errorf("host artifact sizes exceed supported image limits")
	}
	if m.Engine.Docker == "" || m.Engine.Containerd == "" || m.Engine.Driver != "zfs" || m.Engine.DataRoot != "/floatlab/system/docker" {
		return fmt.Errorf("unsupported Docker store configuration")
	}
	for _, source := range m.QualifiedSources {
		if _, err := release.Parse(source); err != nil {
			return fmt.Errorf("invalid qualified source: %w", err)
		}
	}
	return nil
}

func ValidateTransition(source, target Manifest, actual Engine) error {
	if err := source.Validate(); err != nil {
		return fmt.Errorf("source baseline requires recovery bootstrap: %w", err)
	}
	if err := target.Validate(); err != nil {
		return err
	}
	if source.Engine != actual {
		return fmt.Errorf("running Docker configuration differs from source release; refusing activation")
	}
	if source.Bootloader != target.Bootloader || source.Files["zfsbootmenu.EFI"] != target.Files["zfsbootmenu.EFI"] || source.PoolCompatibility != target.PoolCompatibility {
		return fmt.Errorf("bootloader or ZFS compatibility transition requires separate qualification")
	}
	for _, v := range target.QualifiedSources {
		if v == source.Version {
			return nil
		}
	}
	return fmt.Errorf("Docker upgrade and fallback from host-v%s to host-v%s have not been qualified", source.Version, target.Version)
}

var operationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

type Request struct {
	OperationID    string `json:"operation_id"`
	Release        string `json:"release"`
	Actor          string `json:"actor"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

func (r Request) Validate() error {
	if !operationID.MatchString(r.OperationID) || r.Actor == "" || len(r.Actor) > 256 {
		return fmt.Errorf("operation_id and actor are required")
	}
	if !strings.HasPrefix(r.Release, "host-v") {
		return fmt.Errorf("select an explicit host-v release")
	}
	_, err := release.Parse(strings.TrimPrefix(r.Release, "host-v"))
	return err
}
