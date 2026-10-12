package installer

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
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/pelletier/go-toml/v2"
	"golang.org/x/sys/unix"
)

const Runtime = "/run/floatlab-installer"
const Persistent = "/floatlab/system/etc"

type Operation struct {
	Disks    []Disk    `json:"disks"`
	Command  string    `json:"command"`
	Identity Appliance `json:"identity"`
}
type Appliance struct {
	NodeID    string             `json:"node_id"`
	NodeName  string             `json:"node_name"`
	Timezone  string             `json:"timezone"`
	HostID    string             `json:"host_id"`
	JWTSecret string             `json:"jwt_secret"`
	Network   hostnetwork.Config `json:"network"`
}
type Submission struct {
	Host                 Appliance             `json:"host"`
	Admin                credentials.Bootstrap `json:"admin"`
	VerificationCode     string                `json:"verification_code,omitempty"`
	VerificationPassword string                `json:"verification_password"` // encrypted; only retained in /run until success.
}
type Status struct {
	Phase        string    `json:"phase"`
	Completed    []string  `json:"completed"`
	Started      time.Time `json:"started"`
	PoolReady    bool      `json:"pool_ready"`
	StorageReady bool      `json:"storage_ready"`
	Ready        bool      `json:"ready"`
	Error        string    `json:"error,omitempty"`
	Address      string    `json:"address,omitempty"`
}

var hostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func (a Appliance) Validate() error {
	if !hostname.MatchString(a.NodeName) {
		return fmt.Errorf("node name must be a valid lowercase hostname")
	}
	if a.Timezone == "" || filepath.IsAbs(a.Timezone) || strings.Contains(a.Timezone, "..") {
		return fmt.Errorf("invalid timezone")
	}
	if _, err := time.LoadLocation(a.Timezone); err != nil {
		return fmt.Errorf("unknown timezone")
	}
	if len(a.HostID) != 8 || a.HostID == "00000000" {
		return fmt.Errorf("invalid ZFS host ID")
	}
	if _, err := hex.DecodeString(a.HostID); err != nil {
		return err
	}
	if a.NodeID == "" || len(a.JWTSecret) < 32 {
		return fmt.Errorf("invalid node identity")
	}
	return a.Network.Validate()
}
func Lock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("installation already running")
	}
	return f, nil
}
func LoadStatus() (Status, error) {
	var s Status
	err := credentials.ReadPrivate(Runtime+"/status.json", &s)
	return s, err
}
func saveStatus(s Status) error { return saveStatusAt(Runtime, Persistent, s) }
func saveStatusAt(runtimeDir, persistentDir string, s Status) error {
	if err := credentials.WritePrivate(runtimeDir+"/status.json", s); err != nil {
		return err
	}
	if s.PoolReady || s.StorageReady {
		return credentials.WritePrivate(persistentDir+"/install-status.json", s)
	}
	return nil
}
func run(ctx context.Context, name string, args ...string) error {
	_, err := Output(ctx, name, args...)
	return err
}
func copyVerified(source, target, checksum string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(output, hash), input)
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if hex.EncodeToString(hash.Sum(nil)) != checksum {
		return fmt.Errorf("image checksum mismatch for %s", filepath.Base(source))
	}
	// Read back from the destination, rather than only checking the source stream.
	written, err := os.Open(target)
	if err != nil {
		return err
	}
	defer written.Close()
	hash.Reset()
	if _, err = io.Copy(hash, written); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != checksum {
		return fmt.Errorf("written image checksum mismatch")
	}
	return nil
}
func GRUB(p Payload, uuid string, a Appliance) string {
	return fmt.Sprintf("set timeout=3\nserial --unit=0 --speed=115200\nterminal_input console serial\nterminal_output console serial\nmenuentry 'FloatLab' {\n search --no-floppy --fs-uuid --set=root %s\n linux /%s init=%s root=UUID=%s console=ttyS0,115200n8 spl.spl_hostid=0x%s floatlab.installed=1\n initrd /%s\n}\n", uuid, p.Kernel, p.Init, uuid, a.HostID, p.Initrd)
}

// Worker is owned by systemd, not by the console's lifetime. Destructive work is
// allowed only from a newly confirmed operation, never from a saved checkpoint.
func Worker(ctx context.Context) error {
	return worker(ctx, Runtime, Persistent, "/iso", Inventory)
}
func worker(ctx context.Context, runtimeDir, persistentDir, mediaDir string, inventory func(context.Context) ([]Disk, error)) (err error) {
	lock, err := Lock(runtimeDir + "/worker.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	var op Operation
	if err = credentials.ReadPrivate(runtimeDir+"/operation.json", &op); err != nil {
		return err
	}
	var payload Payload
	b, err := os.ReadFile(mediaDir + "/floatlab-manifest.json")
	if err != nil {
		return err
	}
	if err = json.Unmarshal(b, &payload); err != nil {
		return err
	}
	if err = payload.Validate(); err != nil {
		return err
	}
	var status Status
	loadErr := credentials.ReadPrivate(runtimeDir+"/status.json", &status)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return loadErr
	}
	if status.Ready {
		return nil
	}
	if status.Phase != "" && !status.PoolReady && !status.StorageReady {
		return fmt.Errorf("interrupted destructive installation: inspect disks and confirm a new operation")
	}
	if status.Started.IsZero() {
		status.Started = time.Now()
	}
	defer func() {
		if err != nil {
			status.Error = err.Error()
			_ = os.Remove(runtimeDir + "/finalizing")
			if status.PoolReady || status.StorageReady {
				cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				_ = run(cleanup, "systemctl", "stop", "--no-block", "floatlab-core-stack.service")
				cancel()
			}
			_ = saveStatusAt(runtimeDir, persistentDir, status)
		}
	}()
	phase := func(name string, action func() error) error {
		status.Phase = name
		fmt.Println("Installation phase:", name)
		status.Error = ""
		if err := saveStatusAt(runtimeDir, persistentDir, status); err != nil {
			return err
		}
		if err := action(); err != nil {
			return err
		}
		status.Completed = append(status.Completed, name)
		return saveStatusAt(runtimeDir, persistentDir, status)
	}
	if !status.PoolReady && !status.StorageReady {
		if err = ValidateSelection(op.Disks, payload.Bytes); err != nil {
			return err
		}
		args, e := ParseZPool(op.Command, op.Disks)
		if e != nil {
			return e
		}
		current, e := inventory(ctx)
		if e != nil {
			return e
		}
		if e = Revalidate(op.Disks, current); e != nil {
			return e
		}
		if err = phase("Partition and format disks", func() error {
			for _, d := range op.Disks {
				if e := run(ctx, "wipefs", "--all", d.Path); e != nil {
					return e
				}
				cmd := exec.CommandContext(ctx, "sfdisk", "--wipe", "always", d.Path)
				cmd.Stdin = strings.NewReader(PartitionScript(ImageSize(payload.Bytes)))
				if output, e := cmd.CombinedOutput(); e != nil {
					return fmt.Errorf("partition %s: %w: %s", d.Path, e, Diagnostic(output))
				}
				if e := run(ctx, "udevadm", "settle"); e != nil {
					return e
				}
				if e := run(ctx, "mkfs.vfat", "-F", "32", d.Path+"-part1"); e != nil {
					return e
				}
				if e := run(ctx, "mkfs.ext4", "-F", d.Path+"-part2"); e != nil {
					return e
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if err = phase("Create ZFS storage volume", func() error {
			if e := run(ctx, "zpool", args...); e != nil {
				return e
			}
			// Start only the existing dataset reconciliation service; the stack remains gated.
			return run(ctx, "systemctl", "restart", "floatlab-zfs-import.service", "floatlab-datasets.service")
		}); err != nil {
			return err
		}

		for i := range op.Disks {
			for partition := 1; partition <= 3; partition++ {
				id, e := Output(ctx, "blkid", "-s", "PARTUUID", "-o", "value", fmt.Sprintf("%s-part%d", op.Disks[i].Path, partition))
				if e != nil {
					return e
				}
				value := strings.TrimSpace(string(id))
				if value == "" {
					return fmt.Errorf("cannot record partition identity")
				}
				op.Disks[i].PartitionIDs = append(op.Disks[i].PartitionIDs, value)
			}
		}
		if err = credentials.WritePrivate(runtimeDir+"/operation.json", op); err != nil {
			return err
		}
		for _, path := range []string{filepath.Dir(persistentDir), persistentDir} {
			if err = run(ctx, "mountpoint", "-q", path); err != nil {
				return fmt.Errorf("persistent storage is not mounted: %w", err)
			}
		}
		if err = credentials.WritePrivate(persistentDir+"/install-operation.json", op); err != nil {
			return err
		}
		key, e := os.ReadFile(runtimeDir + "/credential.key")
		if e != nil {
			return e
		}
		if len(key) != 32 {
			return fmt.Errorf("invalid credential encryption key")
		}
		if err = credentials.WriteBytes(persistentDir+"/credential.key", key); err != nil {
			return err
		}
		status.PoolReady = true
		if err = saveStatusAt(runtimeDir, persistentDir, status); err != nil {
			return err
		}
	}
	if !status.StorageReady {
		if err = verifyImageTargets(ctx, op.Disks, mediaDir); err != nil {
			return err
		}
		if err = phase("Copy image and install EFI bootloaders", func() error {
			files := make([]string, 0, len(payload.Files))
			for name := range payload.Files {
				files = append(files, name)
			}
			sort.Strings(files)
			for i, d := range op.Disks {
				diskErr := func() (diskErr error) {
					image := fmt.Sprintf("%s/image-%d", runtimeDir, i)
					efi := fmt.Sprintf("%s/efi-%d", runtimeDir, i)
					for _, dir := range []string{image, efi} {
						if e := os.MkdirAll(dir, 0700); e != nil {
							return e
						}
					}
					if e := run(ctx, "mount", d.Path+"-part2", image); e != nil {
						return e
					}
					defer func() {
						cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						diskErr = errors.Join(diskErr, run(cleanup, "umount", image))
					}()
					if e := run(ctx, "mount", d.Path+"-part1", efi); e != nil {
						return e
					}
					defer func() {
						cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
						defer cancel()
						diskErr = errors.Join(diskErr, run(cleanup, "umount", efi))
					}()
					for _, name := range files {
						if e := copyVerified(filepath.Join(mediaDir, name), filepath.Join(image, name), payload.Files[name]); e != nil {
							return e
						}
					}
					uuid, e := Output(ctx, "blkid", "-s", "UUID", "-o", "value", d.Path+"-part2")
					if e != nil {
						return e
					}
					if e = credentials.WriteBytes(image+"/floatlab-manifest.json", b); e != nil {
						return e
					}
					if e = credentials.WriteBytes(image+"/floatlab-host-id", []byte(op.Identity.HostID)); e != nil {
						return e
					}
					if e = credentials.WriteBytes(image+"/boot/grub/grub.cfg", []byte(GRUB(payload, strings.TrimSpace(string(uuid)), op.Identity))); e != nil {
						return e
					}
					if e = run(ctx, "grub-install", "--target=x86_64-efi", "--removable", "--no-nvram", "--efi-directory="+efi, "--boot-directory="+image+"/boot"); e != nil {
						return e
					}
					return nil
				}()
				if diskErr != nil {
					return diskErr
				}
			}
			return nil
		}); err != nil {
			return err
		}
		status.StorageReady = true
		if err = saveStatusAt(runtimeDir, persistentDir, status); err != nil {
			return err
		}
	}
	status.Phase = "Waiting for host settings and credentials"
	status.Error = ""
	if err = saveStatusAt(runtimeDir, persistentDir, status); err != nil {
		return err
	}
	var sub Submission
	for {
		err = credentials.ReadPrivate(runtimeDir+"/submission.json", &sub)
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err = sub.Host.Validate(); err != nil {
		return err
	}
	if err = credentials.ValidateBootstrap(sub.Admin); err != nil {
		return err
	}
	if sub.Host.NodeID != op.Identity.NodeID || sub.Host.HostID != op.Identity.HostID || sub.Host.JWTSecret != op.Identity.JWTSecret || sub.Admin.NodeID != sub.Host.NodeID || sub.Admin.NodeName != sub.Host.NodeName {
		return fmt.Errorf("staged identity does not match confirmed installation")
	}
	if err = phase("Persist and apply host configuration", func() error {
		if e := credentials.WritePrivate(persistentDir+"/appliance.json", sub.Host); e != nil {
			return e
		}
		if e := credentials.WriteBytes(filepath.Dir(persistentDir)+"/.env", []byte("FLOATLAB_NODE_ID="+sub.Host.NodeID+"\n")); e != nil {
			return e
		}
		if e := credentials.WriteBytes(persistentDir+"/hostd.env", []byte("FLOATLAB_NODE_ID="+sub.Host.NodeID+"\n")); e != nil {
			return e
		}
		network, e := toml.Marshal(sub.Host.Network)
		if e != nil {
			return e
		}
		if e = credentials.WriteBytes(persistentDir+"/network.toml", network); e != nil {
			return e
		}
		if _, e = os.Stat(persistentDir + "/bootstrap-imported"); os.IsNotExist(e) {
			if e = credentials.WritePrivate(persistentDir+"/bootstrap.json", sub.Admin); e != nil {
				return e
			}
		}
		// This temporary gate allows the stack to start only during final verification.
		if e = credentials.WriteBytes(runtimeDir+"/finalizing", []byte("1")); e != nil {
			return e
		}
		if e = run(ctx, "hostnamectl", "--transient", "set-hostname", sub.Host.NodeName); e != nil {
			return e
		}
		if e = run(ctx, "timedatectl", "set-timezone", sub.Host.Timezone); e != nil {
			return e
		}
		return run(ctx, "systemctl", "restart", "floatlab-network-config.service")
	}); err != nil {
		return err
	}
	if err = phase("Fetch images and verify FloatLab services", func() error {
		if e := run(ctx, "systemctl", "restart", "floatlab-core-stack.service"); e != nil {
			return e
		}
		return verifyStack(ctx, sub)
	}); err != nil {
		return err
	}
	if err = credentials.WriteBytes(persistentDir+"/installed", []byte("complete\n")); err != nil {
		return err
	}
	if err = os.Remove(runtimeDir + "/submission.json"); err != nil {
		return err
	}
	status.Ready = true
	status.Phase = "Installation complete"
	status.Address = managementAddress(ctx, sub.Host)
	return saveStatusAt(runtimeDir, persistentDir, status)
}
func verifyStack(ctx context.Context, sub Submission) error {
	key, err := os.ReadFile(Persistent + "/credential.key")
	if err != nil {
		return err
	}
	password, err := credentials.Decrypt(key, sub.VerificationPassword)
	if err != nil {
		return err
	}
	request := map[string]string{"username": sub.Admin.Username, "password": password}
	if sub.VerificationCode != "" {
		request["totp_code"] = sub.VerificationCode
	}
	if sub.Admin.TOTPSecret != "" && sub.VerificationCode == "" {
		synced, e := Output(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
		if e != nil || strings.TrimSpace(string(synced)) != "yes" {
			return fmt.Errorf("NTP synchronization required to verify enrolled administrator")
		}
		secret, e := credentials.Decrypt(key, sub.Admin.TOTPSecret)
		if e != nil {
			return e
		}
		code, e := credentials.Code(secret, time.Now().Unix()/30)
		if e != nil {
			return e
		}
		request["totp_code"] = code
	}
	body, _ := json.Marshal(request)
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:8080/api/v1/auth/token", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("administrator verification unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("administrator authentication verification failed")
	}
	var token struct {
		Token string `json:"access_token"`
	}
	if err = json.NewDecoder(response.Body).Decode(&token); err != nil || token.Token == "" {
		return fmt.Errorf("invalid authentication response")
	}
	req, err = http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:8080/api/v1/nodes/"+sub.Host.NodeID+"/health", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	response, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("node health verification unavailable")
	}
	defer response.Body.Close()
	var health struct {
		Status string `json:"status"`
	}
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&health) != nil || health.Status != "reachable" {
		return fmt.Errorf("configured node is not reachable")
	}
	return nil
}
func managementAddress(ctx context.Context, a Appliance) string {
	if a.Network.IPv4.Mode == "static" {
		return "http://" + a.Network.IPv4.Address + ":8080"
	}
	out, err := Output(ctx, "ip", "-json", "-4", "addr", "show", "dev", hostnetwork.Bridge)
	if err == nil {
		var links []struct {
			Addresses []struct {
				Local string `json:"local"`
			} `json:"addr_info"`
		}
		if json.Unmarshal(out, &links) == nil {
			for _, l := range links {
				for _, ip := range l.Addresses {
					if ip.Local != "" {
						return "http://" + ip.Local + ":8080"
					}
				}
			}
		}
	}
	return "DHCP address pending (check the local console)"
}
