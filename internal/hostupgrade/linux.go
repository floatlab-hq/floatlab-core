package hostupgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/installer"
)

type Linux struct {
	Run func(context.Context, string, ...string) (string, error)
}

func (l Linux) output(ctx context.Context, name string, args ...string) (string, error) {
	if l.Run != nil {
		return l.Run(ctx, name, args...)
	}
	b, err := installer.Output(ctx, name, args...)
	return strings.TrimSpace(string(b)), err
}
func (l Linux) run(ctx context.Context, name string, args ...string) error {
	_, err := l.output(ctx, name, args...)
	return err
}
func (l Linux) property(ctx context.Context, key, dataset string) (string, error) {
	return l.output(ctx, "zfs", "get", "-H", "-o", "value", key, dataset)
}
func (l Linux) exists(ctx context.Context, dataset string) bool {
	return l.run(ctx, "zfs", "list", "-H", "-o", "name", dataset) == nil
}

var client = &http.Client{Timeout: 45 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
	if r.URL.Scheme != "https" || len(via) > 5 {
		return fmt.Errorf("unsafe release redirect")
	}
	return nil
}}

func fetch(ctx context.Context, url string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("official release download: %s", res.Status)
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("release artifact exceeds declared size limit")
	}
	return nil
}

func Releases(ctx context.Context) (json.RawMessage, error) {
	var b bytes.Buffer
	if err := fetch(ctx, "https://api.github.com/repos/"+Repository+"/releases", &b, 4<<20); err != nil {
		return nil, err
	}
	var all []struct {
		Tag        string `json:"tag_name"`
		Name       string `json:"name"`
		Prerelease bool   `json:"prerelease"`
	}
	if err := json.Unmarshal(b.Bytes(), &all); err != nil {
		return nil, err
	}
	selected := all[:0]
	for _, r := range all {
		if strings.HasPrefix(r.Tag, "host-v") {
			selected = append(selected, r)
		}
	}
	return json.Marshal(selected)
}

func (l Linux) Stage(ctx context.Context, r Request) (target, source Manifest, err error) {
	b, err := os.ReadFile("/floatlab-manifest.json")
	if err != nil {
		return target, source, err
	}
	if err = json.Unmarshal(b, &source); err != nil {
		return target, source, err
	}
	identityBytes, e := os.ReadFile("/floatlab-host-id")
	if e != nil {
		return target, source, e
	}
	source.InstalledHostID = strings.TrimSpace(string(identityBytes))
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(source.InstalledHostID) {
		return target, source, fmt.Errorf("invalid installed host identity")
	}

	baseURL := "https://github.com/" + Repository + "/releases/download/" + r.Release + "/"
	var manifest bytes.Buffer
	if err = fetch(ctx, baseURL+"host-manifest.json", &manifest, 1<<20); err != nil {
		return target, source, err
	}
	if err = json.Unmarshal(manifest.Bytes(), &target); err != nil {
		return target, source, err
	}
	if err = target.Validate(); err != nil {
		return target, source, err
	}
	if source.Version == target.Version {
		return target, source, fmt.Errorf("selected host version is already running")
	}
	if r.Release != "host-v"+target.Version {
		return target, source, fmt.Errorf("release/version mismatch")
	}
	health, e := l.output(ctx, "zpool", "list", "-H", "-o", "health", "floatlab")
	if e != nil || health != "ONLINE" {
		return target, source, fmt.Errorf("pool must be ONLINE before staging")
	}
	free, e := l.output(ctx, "zfs", "get", "-Hp", "-o", "value", "available", "floatlab/os")
	if e != nil {
		return target, source, e
	}
	available, e := strconv.ParseInt(free, 10, 64)
	if e != nil || available < target.RootBytes+2*target.Bytes+2*installer.GiB {
		return target, source, fmt.Errorf("insufficient staging space")
	}
	dir := filepath.Join(Directory, r.OperationID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return target, source, err
	}
	archive := filepath.Join(dir, "floatlab-root.tar.gz")
	part := archive + ".partial"
	f, e := os.OpenFile(part, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return target, source, e
	}
	hash := sha256.New()
	e = fetch(ctx, baseURL+"floatlab-root.tar.gz", io.MultiWriter(f, hash), target.Bytes)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return target, source, e
	}
	if hex.EncodeToString(hash.Sum(nil)) != target.Files["floatlab-root.tar.gz"] {
		return target, source, fmt.Errorf("root archive checksum mismatch")
	}
	if err = os.Rename(part, archive); err != nil {
		return target, source, err
	}
	dataset := "floatlab/os/" + target.Version
	snapshot := dataset + "@base"
	mountpoint := filepath.Join("/run/floatlab-updates", r.OperationID, "os")
	if l.exists(ctx, dataset) {
		owner, e := l.property(ctx, "org.floatlab:update", dataset)
		if e != nil || !operationID.MatchString(owner) || !l.exists(ctx, snapshot) {
			return target, source, fmt.Errorf("existing OS dataset is incomplete or foreign; explicit cleanup required")
		}
		readonly, e := l.property(ctx, "readonly", dataset)
		if e != nil || readonly != "on" {
			return target, source, fmt.Errorf("existing OS dataset is writable")
		}
		if err = os.MkdirAll(mountpoint, 0700); err != nil {
			return target, source, err
		}
		if err = l.run(ctx, "mount", "-t", "zfs", "-o", "ro", dataset, mountpoint); err != nil {
			return target, source, err
		}
		written, e := os.ReadFile(mountpoint + "/floatlab-manifest.json")
		id, idErr := os.ReadFile(mountpoint + "/floatlab-host-id")
		hostID, hostErr := os.ReadFile("/floatlab-host-id")
		unmountErr := l.run(context.WithoutCancel(ctx), "umount", mountpoint)
		if e = errors.Join(e, idErr, hostErr, unmountErr); e != nil {
			return target, source, e
		}
		if !bytes.Equal(written, manifest.Bytes()) || !bytes.Equal(id, hostID) {
			return target, source, fmt.Errorf("existing OS content differs from release or host identity")
		}
	} else {
		if err = l.run(ctx, "zfs", "create", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "org.zfsbootmenu:active=off", "-o", "org.floatlab:update="+r.OperationID, dataset); err != nil {
			return target, source, err
		}
		if err = os.MkdirAll(mountpoint, 0700); err != nil {
			return target, source, err
		}
		if err = l.run(ctx, "mount", "-t", "zfs", dataset, mountpoint); err != nil {
			return target, source, err
		}
		err = func() (e error) {
			defer func() { e = errors.Join(e, l.run(context.WithoutCancel(ctx), "umount", mountpoint)) }()
			hostID, e := os.ReadFile("/floatlab-host-id")
			if e != nil {
				return e
			}
			if len(strings.TrimSpace(string(hostID))) != 8 {
				return fmt.Errorf("invalid running host identity")
			}
			if e = installer.PopulateOSRoot(ctx, mountpoint, archive, target.Payload, strings.TrimSpace(string(hostID)), manifest.Bytes(), nil); e != nil {
				return e
			}
			systemPath := strings.TrimSuffix(target.Init, "/init")
			if e = l.run(ctx, "chroot", mountpoint, systemPath+"/sw/bin/test", "-e", systemPath+"/etc/systemd/system/sysinit.target.wants/floatlab-boot-recovery.service"); e != nil {
				return fmt.Errorf("target release has no enabled independent boot guard: %w", e)
			}
			return l.run(ctx, "zfs", "snapshot", snapshot)
		}()
		if err != nil {
			return target, source, err
		}

	}
	if err = l.run(ctx, "zfs", "set", "readonly=on", dataset); err != nil {
		return target, source, err
	}
	hostID, err := os.ReadFile("/floatlab-host-id")
	if err != nil {
		return target, source, err
	}
	identity := strings.TrimSpace(string(hostID))
	if err = l.run(ctx, "zfs", "set", "org.floatlab:host-id="+identity, "org.floatlab:version="+target.Version, "org.floatlab:init="+target.Init, dataset); err != nil {
		return target, source, err
	}
	if l.exists(ctx, "floatlab/os/next") {
		owner, e := l.property(ctx, "org.floatlab:update", "floatlab/os/next")
		origin, originErr := l.property(ctx, "origin", "floatlab/os/next")
		if e != nil || originErr != nil || owner != r.OperationID || origin != snapshot {
			return target, source, fmt.Errorf("next boot dataset belongs to another operation")
		}
		return target, source, nil
	}
	if err = l.run(ctx, "zfs", "clone", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "readonly=on", "-o", "org.zfsbootmenu:active=on", "-o", "org.floatlab:update="+r.OperationID, snapshot, "floatlab/os/next"); err != nil {
		return target, source, err
	}
	cmdline := "init=" + target.Init + " console=ttyS0,115200n8 spl.spl_hostid=0x" + strings.TrimSpace(string(hostID)) + " floatlab.installed=1 floatlab.trial=" + r.OperationID
	err = l.run(ctx, "zfs", "set", "org.floatlab:host-id="+identity, "org.floatlab:version="+target.Version, "org.floatlab:init="+target.Init, "org.zfsbootmenu:commandline="+cmdline, "org.zfsbootmenu:rootprefix=root=zfs:", "floatlab/os/next")
	return target, source, err
}

func (l Linux) actualEngine(ctx context.Context) (Engine, error) {
	var info struct {
		Driver        string
		DockerRootDir string
		ServerVersion string
	}
	b, err := l.output(ctx, "docker", "info", "--format", "{{json .}}")
	if err != nil {
		return Engine{}, err
	}
	if err = json.Unmarshal([]byte(b), &info); err != nil {
		return Engine{}, err
	}
	var version struct {
		Server struct {
			Components []struct{ Name, Version string }
		}
	}
	b, err = l.output(ctx, "docker", "version", "--format", "{{json .}}")
	if err != nil {
		return Engine{}, err
	}
	if err = json.Unmarshal([]byte(b), &version); err != nil {
		return Engine{}, err
	}
	containerd := ""
	for _, c := range version.Server.Components {
		if c.Name == "containerd" {
			containerd = strings.TrimPrefix(c.Version, "v")
		}
	}
	return Engine{Docker: info.ServerVersion, Containerd: containerd, Driver: info.Driver, DataRoot: info.DockerRootDir}, nil
}

func (l Linux) ValidateActivation(ctx context.Context, s Status) error {
	engine, err := l.actualEngine(ctx)
	if err != nil {
		return err
	}
	if err = ValidateTransition(s.Source, s.Manifest, engine); err != nil {
		return err
	}
	if err = l.run(ctx, "systemctl", "is-enabled", "floatlab-boot-recovery.service"); err != nil {
		return fmt.Errorf("independent boot recovery is not enabled: %w", err)
	}
	compat, err := l.output(ctx, "zpool", "get", "-H", "-o", "value", "compatibility", "floatlab")
	if err != nil || compat != s.Source.PoolCompatibility {
		return fmt.Errorf("pool compatibility differs from release")
	}
	if _, err = os.Stat("/sys/firmware/efi/efivars"); err != nil {
		return fmt.Errorf("UEFI one-shot boot variables unavailable")
	}
	return nil
}

func (l Linux) Running(ctx context.Context) (version, origin, token, boot string, err error) {
	b, err := os.ReadFile("/floatlab-manifest.json")
	if err != nil {
		return
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return
	}
	version = m.Version
	origin, err = l.property(ctx, "origin", "floatlab/os/live")
	if err != nil {
		return
	}
	b, err = os.ReadFile("/proc/cmdline")
	if err != nil {
		return
	}
	token, err = TrialToken(string(b))
	if err != nil {
		return
	}
	if token != "" {
		var firmware string
		firmware, err = l.output(ctx, "efibootmgr")
		if err != nil {
			return
		}
		if strings.Contains(firmware, "BootNext:") {
			err = fmt.Errorf("firmware did not consume one-shot BootNext")
			return
		}
	}

	b, err = os.ReadFile("/proc/sys/kernel/random/boot_id")
	boot = strings.TrimSpace(string(b))
	return
}
func (l Linux) Reboot(ctx context.Context) error { return l.run(ctx, "systemctl", "reboot") }
func (l Linux) Clear(ctx context.Context) error {
	value, err := l.output(ctx, "efibootmgr")
	if err != nil {
		return err
	}
	if strings.Contains(value, "BootNext:") {
		return l.run(ctx, "efibootmgr", "-N")
	}
	return nil
}

func (l Linux) Health(ctx context.Context) error {
	for _, name := range []string{"floatlab-network-config.service", "docker.service", "floatlab-hostd.service"} {
		if err := l.run(ctx, "systemctl", "is-active", name); err != nil {
			return err
		}
	}
	for _, path := range []string{"/floatlab/system", "/floatlab/system/management/rqlite", "/floatlab/system/management/raft", "/floatlab/system/management/victoria-metrics", "/floatlab/system/management/victoria-logs"} {
		if err := l.run(ctx, "mountpoint", "-q", path); err != nil {
			return err
		}
	}
	if _, err := os.Stat("/run/floatlab/hostd.sock"); err != nil {
		return err
	}
	for _, url := range []string{"http://127.0.0.1:8080/api/v1/health/ready", "http://127.0.0.1:8428/health", "http://127.0.0.1:9428/health"} {
		request, e := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if e != nil {
			return e
		}
		res, e := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if e != nil {
			return e
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			return fmt.Errorf("trial health %s: %s", url, res.Status)
		}
	}
	id, err := l.output(ctx, "docker", "ps", "-q", "--filter", "label=com.docker.compose.project=floatlab-system", "--filter", "label=com.docker.compose.service=floatlab-control")
	if err != nil || id == "" {
		return fmt.Errorf("trial management container unavailable")
	}
	return l.run(ctx, "docker", "exec", id, "/usr/local/bin/floatlab-control", "--self-test", "--rqlite-url=http://rqlite:4001", "--vlogs-url=http://victoria-logs:9428", "--vmetrics-url=http://victoria-metrics:8428")
}

func (l Linux) Promote(ctx context.Context, s Status) error {
	target := "floatlab/os/" + s.Manifest.Version + "@base"
	source := "floatlab/os/" + s.Source.Version + "@base"
	if current, err := l.property(ctx, "origin", "floatlab/os/current"); err == nil && current == target {
		return l.run(ctx, "zpool", "set", "bootfs=floatlab/os/current", "floatlab")
	}
	if l.exists(ctx, "floatlab/os/recovery") {
		origin, err := l.property(ctx, "origin", "floatlab/os/recovery")
		if err != nil {
			return err
		}
		if origin != source {
			owner, err := l.property(ctx, "org.floatlab:update", "floatlab/os/recovery")
			if err != nil || !operationID.MatchString(owner) {
				return fmt.Errorf("foreign recovery boot dataset")
			}
			if err = l.run(ctx, "zfs", "destroy", "floatlab/os/recovery"); err != nil {
				return err
			}
		}
	}
	// Keep a bootable source alias throughout interrupted current/previous renames.
	if !l.exists(ctx, "floatlab/os/recovery") {
		if err := l.run(ctx, "zfs", "clone", "-o", "mountpoint=legacy", "-o", "canmount=noauto", "-o", "readonly=on", "-o", "org.zfsbootmenu:active=on", "-o", "org.floatlab:update="+s.OperationID, source, "floatlab/os/recovery"); err != nil {
			return err
		}
	}
	identity := s.Source.InstalledHostID
	if !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(identity) {
		return fmt.Errorf("invalid source boot identity")
	}
	cmdline := "init=" + s.Source.Init + " console=ttyS0,115200n8 spl.spl_hostid=0x" + identity + " floatlab.installed=1"
	if err := l.run(ctx, "zfs", "set", "org.floatlab:host-id="+identity, "org.floatlab:version="+s.Source.Version, "org.floatlab:init="+s.Source.Init, "org.zfsbootmenu:commandline="+cmdline, "org.zfsbootmenu:rootprefix=root=zfs:", "floatlab/os/recovery"); err != nil {
		return err
	}
	if err := l.run(ctx, "zpool", "set", "bootfs=floatlab/os/recovery", "floatlab"); err != nil {
		return err
	}
	if l.exists(ctx, "floatlab/os/current") {
		origin, err := l.property(ctx, "origin", "floatlab/os/current")
		if err != nil || origin != source {
			return fmt.Errorf("current boot origin changed during promotion")
		}
		if l.exists(ctx, "floatlab/os/previous") {
			owner, err := l.property(ctx, "org.floatlab:update", "floatlab/os/previous")
			if err != nil || owner == "-" || owner == "" {
				return fmt.Errorf("foreign previous boot dataset")
			}
			// Retain the older alias until promotion is durably committed.
			if err = l.run(ctx, "zfs", "rename", "floatlab/os/previous", "floatlab/os/retired-"+s.OperationID); err != nil {
				return err
			}
		}
		if err = l.run(ctx, "zfs", "set", "org.floatlab:update="+s.OperationID, "floatlab/os/current"); err != nil {
			return err
		}
		if err := l.run(ctx, "zfs", "rename", "floatlab/os/current", "floatlab/os/previous"); err != nil {
			return err
		}
	}
	origin, err := l.property(ctx, "origin", "floatlab/os/next")
	if err != nil || origin != target {
		return fmt.Errorf("next boot origin mismatch")
	}
	if err = l.run(ctx, "zfs", "rename", "floatlab/os/next", "floatlab/os/current"); err != nil {
		return err
	}
	return l.run(ctx, "zpool", "set", "bootfs=floatlab/os/current", "floatlab")
}

func (l Linux) Cleanup(ctx context.Context, s Status) error {
	// Deletion deliberately uses non-recursive zfs destroy; holds/clones remain a veto.
	rows, err := l.output(ctx, "zfs", "list", "-H", "-d", "1", "-o", "name", "floatlab/os")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(rows) {
		retired := strings.TrimPrefix(name, "floatlab/os/retired-")
		eligible := retired != name && operationID.MatchString(retired) && s.State == "committed"
		if name == "floatlab/os/next" && (s.State == "recovered" || s.State == "failed") {
			eligible = true
		}
		if !eligible {
			continue
		}
		owner, e := l.property(ctx, "org.floatlab:update", name)
		if e != nil {
			return e
		}
		if !operationID.MatchString(owner) {
			return fmt.Errorf("foreign retired boot alias")
		}
		if name == "floatlab/os/next" && owner != s.OperationID {
			return fmt.Errorf("next boot belongs to another operation")
		}
		if e = l.run(ctx, "zfs", "destroy", name); e != nil {
			return e
		}
	}
	protected := map[string]bool{}
	for _, alias := range []string{"current", "previous", "next", "live", "recovery"} {
		if l.exists(ctx, "floatlab/os/"+alias) {
			origin, err := l.property(ctx, "origin", "floatlab/os/"+alias)
			if err != nil {
				return err
			}
			protected[strings.TrimSuffix(origin, "@base")] = true
		}
	}
	names, err := l.output(ctx, "zfs", "list", "-H", "-d", "1", "-o", "name", "floatlab/os")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(names) {
		if protected[name] || name == "floatlab/os" {
			continue
		}
		owner, e := l.property(ctx, "org.floatlab:update", name)
		if e != nil {
			return e
		}
		if !operationID.MatchString(owner) {
			continue
		}
		if name == "floatlab/os/next" || name == "floatlab/os/recovery" {
			continue
		}
		if strings.Contains(strings.TrimPrefix(name, "floatlab/os/"), "/") {
			continue
		}
		if _, e = releaseVersion(name); e != nil {
			continue
		}
		if l.exists(ctx, name+"@base") {
			if e = l.run(ctx, "zfs", "destroy", name+"@base"); e != nil {
				return e
			}
		}
		if e = l.run(ctx, "zfs", "destroy", name); e != nil {
			return e
		}
		// Delete only this version's updater-owned download directory, never arbitrary paths.
		if owner != s.OperationID && operationID.MatchString(owner) {
			for _, file := range []string{"floatlab-root.tar.gz", "floatlab-root.tar.gz.partial"} {
				if e = os.Remove(filepath.Join(Directory, owner, file)); e != nil && !os.IsNotExist(e) {
					return e
				}
			}
		}
	}
	return nil
}

func (l Linux) FinalizeCommit(ctx context.Context, s Status) error {
	origin, err := l.property(ctx, "origin", "floatlab/os/current")
	if err != nil || origin != "floatlab/os/"+s.Manifest.Version+"@base" {
		return fmt.Errorf("committed boot origin mismatch")
	}
	cmdline, err := l.property(ctx, "org.zfsbootmenu:commandline", "floatlab/os/current")
	if err != nil {
		return err
	}
	parts := []string{}
	for _, part := range strings.Fields(cmdline) {
		if !strings.HasPrefix(part, "floatlab.trial=") {
			parts = append(parts, part)
		}
	}
	if err = l.run(ctx, "zfs", "set", "org.zfsbootmenu:commandline="+strings.Join(parts, " "), "floatlab/os/current"); err != nil {
		return err
	}
	return WriteGuardMessage(s.OperationID, "committed")
}

func (l Linux) RestoreBoot(ctx context.Context, s Status) error {
	source := "floatlab/os/" + s.Source.Version + "@base"
	if l.exists(ctx, "floatlab/os/current") {
		origin, err := l.property(ctx, "origin", "floatlab/os/current")
		if err != nil {
			return err
		}
		if origin == source {
			return l.run(ctx, "zpool", "set", "bootfs=floatlab/os/current", "floatlab")
		}
		if origin != "floatlab/os/"+s.Manifest.Version+"@base" {
			return fmt.Errorf("unexpected current boot origin")
		}
		recovery, err := l.property(ctx, "origin", "floatlab/os/recovery")
		if err != nil || recovery != source {
			return fmt.Errorf("source recovery alias unavailable")
		}
		if err = l.run(ctx, "zpool", "set", "bootfs=floatlab/os/recovery", "floatlab"); err != nil {
			return err
		}
		if l.exists(ctx, "floatlab/os/next") {
			return fmt.Errorf("cannot restore current while next is occupied")
		}
		if err = l.run(ctx, "zfs", "rename", "floatlab/os/current", "floatlab/os/next"); err != nil {
			return err
		}
	}
	for _, alias := range []string{"previous", "recovery"} {
		if l.exists(ctx, "floatlab/os/"+alias) {
			origin, err := l.property(ctx, "origin", "floatlab/os/"+alias)
			if err != nil {
				return err
			}
			if origin == source {
				if err = l.run(ctx, "zfs", "rename", "floatlab/os/"+alias, "floatlab/os/current"); err != nil {
					return err
				}
				return l.run(ctx, "zpool", "set", "bootfs=floatlab/os/current", "floatlab")
			}
		}
	}
	return fmt.Errorf("known-good boot alias unavailable; management remains stopped")
}

func releaseVersion(name string) (string, error) {
	v := strings.TrimPrefix(name, "floatlab/os/")
	r := Request{OperationID: "cleanup", Actor: "hostd", Release: "host-v" + v}
	return v, r.Validate()
}
