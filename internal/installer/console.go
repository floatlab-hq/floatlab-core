package installer

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

type Console struct {
	In  *bufio.Reader
	Out io.Writer
	FD  int
	mu  sync.Mutex
}

func (c *Console) print(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(c.Out, format, args...)
}
func (c *Console) prompt(label, def string) (string, error) {
	c.print("%s", label)
	if def != "" {
		c.print(" [%s]", def)
	}
	c.print(": ")
	line, err := c.In.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	return line, nil
}
func (c *Console) password(label string) (string, error) {
	c.print("%s: ", label)
	if term.IsTerminal(c.FD) {
		b, err := term.ReadPassword(c.FD)
		c.print("\n")
		return string(b), err
	}
	line, err := c.In.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}
func (c *Console) progress(ctx context.Context, runtimeDir string) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	lastUpdate := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var s Status
			err := credentials.ReadPrivate(runtimeDir+"/status.json", &s)
			if err == nil && s.Error == "" && (s.Phase != last || time.Since(lastUpdate) > 15*time.Second) {
				c.print("\n[Installation: %s; elapsed %s]\n", s.Phase, time.Since(s.Started).Round(time.Second))
				if s.Phase != last && len(s.Completed) > 0 {
					c.print("Completed: %s\n", strings.Join(s.Completed, ", "))
				}
				last = s.Phase
				lastUpdate = time.Now()
			}
		}
	}
}
func (c *Console) settings(a Appliance) (Appliance, error) {
	c.print("\nStep 2 — Host Settings\n")
	for {
		var err error
		timezone := a.Timezone
		if timezone == "" {
			timezone = "UTC"
		}
		a.Timezone, err = c.prompt("Timezone", timezone)
		if err != nil {
			return a, err
		}
		nodeName := a.NodeName
		if nodeName == "" {
			nodeName = "floatlab"
		}
		a.NodeName, err = c.prompt("Node name", nodeName)
		if err != nil {
			return a, err
		}
		if a.Network.Version != 1 {
			a.Network = hostnetwork.DefaultConfig()
		}
		a.Network.IPv4.Mode, err = c.prompt("IPv4 mode (dhcp/static)", a.Network.IPv4.Mode)
		if err != nil {
			return a, err
		}
		if a.Network.IPv4.Mode == "static" {
			for _, field := range []struct {
				label string
				dest  *string
			}{{"IPv4 address", &a.Network.IPv4.Address}, {"Netmask", &a.Network.IPv4.Netmask}, {"Gateway", &a.Network.IPv4.Gateway}} {
				if *field.dest, err = c.prompt(field.label, *field.dest); err != nil {
					return a, err
				}
			}
			dns, e := c.prompt("DNS servers (comma separated)", strings.Join(a.Network.DNSServers, ","))
			if e != nil {
				return a, e
			}
			a.Network.DNSServers = strings.Split(dns, ",")
			for i := range a.Network.DNSServers {
				a.Network.DNSServers[i] = strings.TrimSpace(a.Network.DNSServers[i])
			}
		}
		if a.Network.IPv4.Mode == "dhcp" {
			a.Network.IPv4 = hostnetwork.IPv4{Mode: "dhcp"}
		}
		if err = a.Validate(); err != nil {
			c.print("Invalid settings: %s\n", err)
			continue
		}
		return a, nil
	}
}
func (c *Console) administrator(ctx context.Context, a Appliance, key []byte, persistentDir string) (Submission, error) {
	c.print("\nStep 3 — Credentials\n")
	var sub Submission
	sub.Host = a
	sub.Admin.NodeID = a.NodeID
	sub.Admin.NodeName = a.NodeName
	for {
		username, err := c.prompt("Administrator username", "admin")
		if err != nil {
			return sub, err
		}
		password, err := c.password("Password")
		if err != nil {
			return sub, err
		}
		confirmation, err := c.password("Confirm password")
		if err != nil {
			return sub, err
		}
		if password != confirmation || !credentials.Valid(username, password) {
			c.print("Passwords must match; username must be 1–64 characters and password 1–72 bytes.\n")
			continue
		}
		hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if e != nil {
			return sub, e
		}
		sub.Admin.Username = username
		sub.Admin.PasswordHash = string(hash)
		sub.VerificationPassword, e = credentials.Encrypt(key, password)
		if e != nil {
			return sub, e
		}
		break
	}
	// If a prior finalization imported this account, enrollment cannot replace it.
	if _, err := os.Stat(persistentDir + "/bootstrap-imported"); err == nil {
		code, e := c.prompt("Existing TOTP code (blank for password-only account)", "")
		if e != nil {
			return sub, e
		}
		sub.VerificationCode = code
		return sub, nil
	}
	synced, err := Output(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
	if err != nil || strings.TrimSpace(string(synced)) != "yes" {
		c.print("NTP is not synchronized. TOTP enrollment is unavailable; continuing with password authentication.\n")
		return sub, nil
	}
	enabled, err := c.prompt("Set up TOTP? (yes/no)", "no")
	if err != nil {
		return sub, err
	}
	if enabled != "yes" {
		return sub, nil
	}
	secret, err := credentials.Secret()
	if err != nil {
		return sub, err
	}
	c.print("Authenticator URI: %s\nManual secret: %s\n", credentials.URI(secret, sub.Admin.Username, a.NodeName), secret)
	for {
		code, e := c.prompt("Six-digit authenticator code", "")
		if e != nil {
			return sub, e
		}
		synchronized, e := Output(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
		if e != nil || strings.TrimSpace(string(synchronized)) != "yes" {
			return sub, fmt.Errorf("NTP synchronization lost; repeat credential enrollment")
		}
		if _, ok := credentials.Verify(secret, code, time.Now(), -1); ok {
			break
		}
		c.print("Invalid or expired code. Try again.\n")
	}
	codes, hashes, err := credentials.RecoveryCodes()
	if err != nil {
		return sub, err
	}
	c.print("Save these single-use recovery codes:\n")
	for _, code := range codes {
		c.print("%s\n", code)
	}
	for {
		answer, e := c.prompt("Type SAVED after recording the recovery codes", "")
		if e != nil {
			return sub, e
		}
		if answer == "SAVED" {
			break
		}
	}
	sub.Admin.RecoveryHashes = hashes
	sub.Admin.TOTPSecret, err = credentials.Encrypt(key, secret)
	return sub, err
}
func (c *Console) Run(ctx context.Context) error { return c.run(ctx, Runtime, Persistent) }
func (c *Console) run(ctx context.Context, runtimeDir, persistentDir string) error {
	c.print("FloatLab installer — press Enter to open the console.\n")
	if _, err := c.In.ReadString('\n'); err != nil {
		return err
	}
	lock, err := Lock(runtimeDir + "/console.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if _, err = os.Stat(persistentDir + "/installed"); err == nil {
		c.print("FloatLab is already installed.\n")
		return nil
	}
	var op Operation
	var saved Status
	e := credentials.ReadPrivate(runtimeDir+"/status.json", &saved)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	active := workerActive(ctx)
	if !active && saved.Phase == "" {
		if _, err = os.Stat(persistentDir + "/install-operation.json"); err == nil {
			return fmt.Errorf("interrupted storage checkpoint; inspect the partial installation before continuing")
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if saved.StorageReady || saved.PoolReady || active {
		if err = credentials.ReadPrivate(runtimeDir+"/operation.json", &op); os.IsNotExist(err) {
			err = credentials.ReadPrivate(persistentDir+"/install-operation.json", &op)
			if err == nil {
				err = credentials.WritePrivate(runtimeDir+"/operation.json", op)
			}
		}
		if err != nil {
			return err
		}
		c.print("Recovering an incomplete installation. Storage will not be erased again.\n")
	} else {
		if saved.Phase != "" {
			return fmt.Errorf("interrupted disk preparation; inspect/export the partial pool before creating a newly confirmed installation")
		}
		if _, e = os.Stat("/sys/firmware/efi"); e != nil {
			return fmt.Errorf("boot the installation media in UEFI mode")
		}
		secure, _ := os.ReadDir("/sys/firmware/efi/efivars")
		for _, f := range secure {
			if strings.HasPrefix(f.Name(), "SecureBoot-") {
				b, e := os.ReadFile("/sys/firmware/efi/efivars/" + f.Name())
				if e != nil {
					return e
				}
				if len(b) > 4 && b[4] != 0 {
					return fmt.Errorf("disable Secure Boot before installing")
				}
			}
		}
		var payload Payload
		b, e := os.ReadFile("/iso/floatlab-manifest.json")
		if e != nil {
			return e
		}
		if e = json.Unmarshal(b, &payload); e != nil {
			return e
		}
		if e = payload.Validate(); e != nil {
			return e
		}
		c.print("\nStep 1 — Create Storage Volume\n")
		disks, e := Inventory(ctx)
		if e != nil {
			return e
		}
		for i, d := range disks {
			c.print("%d. %s | %s | serial %s | %.1f GiB", i+1, d.Path, d.Model, d.Serial, float64(d.Size)/float64(GiB))
			if !d.Eligible {
				c.print(" — unavailable: %s", d.Reason)
			}
			c.print("\n")
		}
		for {
			selected, e := c.prompt("Disk numbers (comma separated)", "")
			if e != nil {
				return e
			}
			op.Disks = nil
			for _, v := range strings.Split(selected, ",") {
				n, e := strconv.Atoi(strings.TrimSpace(v))
				if e != nil || n < 1 || n > len(disks) {
					op.Disks = nil
					break
				}
				op.Disks = append(op.Disks, disks[n-1])
			}
			if e = ValidateSelection(op.Disks, payload.Bytes); e != nil {
				c.print("%s\n", e)
				continue
			}
			break
		}
		smallest := op.Disks[0].Size
		for _, d := range op.Disks {
			if d.Size < smallest {
				smallest = d.Size
			}
			c.print("\n%s — current partition table:\n%s", d.Path, d.PartitionTable)
		}
		c.print("\nProposed GPT layout on each disk: 512 MiB EFI, %d GiB image, remainder ZFS.\nUsable ZFS capacity approximately %.1f GiB", ImageSize(payload.Bytes)/GiB, float64(smallest-ImageSize(payload.Bytes)-512*MiB-2*MiB)/float64(GiB))
		if len(op.Disks) > 1 {
			c.print(" (one mirror, limited by smallest member)")
		}
		c.print("\n")
		for {
			op.Command, e = c.prompt("ZFS command (edit ashift, autotrim, compression, atime only)", ZPoolCommand(op.Disks))
			if e != nil {
				return e
			}
			if _, e = ParseZPool(op.Command, op.Disks); e != nil {
				c.print("%s\n", e)
				continue
			}
			break
		}
		c.print("WARNING: ALL DATA ON THE LISTED DISKS WILL BE ERASED.\n")
		answer, e := c.prompt("Type ERASE ALL DATA to confirm", "")
		if e != nil {
			return e
		}
		if answer != "ERASE ALL DATA" {
			return fmt.Errorf("installation cancelled; no disks erased")
		}
		current, e := Inventory(ctx)
		if e != nil {
			return e
		}
		if e = Revalidate(op.Disks, current); e != nil {
			return e
		}
		hostID, e := credentials.Random(4)
		if e != nil {
			return e
		}
		if hex.EncodeToString(hostID) == "00000000" {
			hostID[0] = 1
		}
		jwt, e := credentials.Random(32)
		if e != nil {
			return e
		}
		op.Identity = Appliance{NodeID: uuid.NewString(), HostID: hex.EncodeToString(hostID), JWTSecret: hex.EncodeToString(jwt)}
		if err = credentials.WritePrivate(runtimeDir+"/operation.json", op); err != nil {
			return err
		}
		key, e := credentials.Random(32)
		if e != nil {
			return e
		}
		if err = credentials.WriteBytes(runtimeDir+"/credential.key", key); err != nil {
			return err
		}
	}
	key, err := os.ReadFile(runtimeDir + "/credential.key")
	if os.IsNotExist(err) {
		key, err = os.ReadFile(persistentDir + "/credential.key")
		if err == nil {
			err = credentials.WriteBytes(runtimeDir+"/credential.key", key)
		}
	}
	if err != nil {
		return err
	}
	if !active && saved.PoolReady && !saved.StorageReady {
		if err = verifyImageTargets(ctx, op.Disks, "/iso"); err != nil {
			return err
		}
	}
	if !active {
		if err = startWorker(ctx); err != nil {
			return err
		}
	}
	progressCtx, cancel := context.WithCancel(ctx)
	progressDone := make(chan struct{})
	go func() { defer close(progressDone); c.progress(progressCtx, runtimeDir) }()
	defer func() { cancel(); <-progressDone }()
	if _, err = os.Stat(runtimeDir + "/submission.json"); os.IsNotExist(err) {
		defaults := op.Identity
		var staged Appliance
		for _, path := range []string{runtimeDir + "/host-settings.json", persistentDir + "/appliance.json"} {
			if e := credentials.ReadPrivate(path, &staged); e == nil {
				if staged.NodeID != op.Identity.NodeID || staged.HostID != op.Identity.HostID || staged.JWTSecret != op.Identity.JWTSecret {
					return fmt.Errorf("staged host identity changed")
				}
				defaults = staged
				break
			} else if !os.IsNotExist(e) {
				return e
			}
		}
		host, e := c.settings(defaults)
		if e != nil {
			return e
		}
		if e = credentials.WritePrivate(runtimeDir+"/host-settings.json", host); e != nil {
			return e
		}
		sub, e := c.administrator(ctx, host, key, persistentDir)
		if e != nil {
			return e
		}
		if e = credentials.WritePrivate(runtimeDir+"/submission.json", sub); e != nil {
			return e
		}
	}
	c.print("\nStep 4 — Wait for installation and verification\n")
	for {
		var status Status
		e := credentials.ReadPrivate(runtimeDir+"/status.json", &status)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if status.Error != "" {
			c.print("Installation failed: %s\n", status.Error)
			if !status.StorageReady && !status.PoolReady {
				return fmt.Errorf("disk preparation failed; no automatic erase retry is allowed")
			}
			answer, e := c.prompt("Retry finalization? (yes/no)", "no")
			if e != nil {
				return e
			}
			if answer != "yes" {
				return fmt.Errorf("installation remains incomplete")
			}
			// Recollect proof when a consumed/expired second factor caused verification to fail.
			if _, e = os.Stat(persistentDir + "/bootstrap-imported"); e == nil {
				var sub Submission
				if e = credentials.ReadPrivate(runtimeDir+"/submission.json", &sub); e != nil {
					return e
				}
				password, e := c.password("Existing administrator password")
				if e != nil {
					return e
				}
				sub.VerificationPassword, e = credentials.Encrypt(key, password)
				if e != nil {
					return e
				}
				sub.VerificationCode, e = c.prompt("Existing TOTP code (blank for password-only)", "")
				if e != nil {
					return e
				}
				sub.Admin.TOTPSecret = ""
				if e = credentials.WritePrivate(runtimeDir+"/submission.json", sub); e != nil {
					return e
				}
			}
			status.Error = ""
			if e = saveStatusAt(runtimeDir, persistentDir, status); e != nil {
				return e
			}
			if e = startWorker(ctx); e != nil {
				return e
			}
		}
		if status.Ready {
			cancel()
			var installed Appliance
			_ = credentials.ReadPrivate(persistentDir+"/appliance.json", &installed)
			c.print("\nInstallation complete. Node: %s\nManagement: %s\n", installed.NodeName, status.Address)
			c.print("Remove installation media before rebooting from the installed disks.\n")
			return c.countdown(ctx)
		}
		// A worker killed before publishing an error must not leave the console waiting forever.
		if status.Error == "" {
			active, e := Output(ctx, "systemctl", "show", "-p", "ActiveState", "--value", "floatlab-install-worker.service")
			if e == nil && strings.TrimSpace(string(active)) == "failed" {
				return fmt.Errorf("installation worker failed; inspect journalctl -u floatlab-install-worker")
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
func (c *Console) countdown(ctx context.Context) error {
	c.print("Rebooting in 30 seconds. Press Enter to cancel.\n")
	input := make(chan error, 1)
	go func() { _, err := c.In.ReadString('\n'); input <- err }()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-input:
		if err != nil {
			return err
		}
		c.print("Automatic reboot cancelled.\n")
		for {
			answer, err := c.prompt("Type REBOOT to reboot when ready", "")
			if err != nil {
				return err
			}
			if answer == "REBOOT" {
				return reboot(ctx)
			}
		}
	case <-timer.C:
		return reboot(ctx)
	}
}

// Export cleanly so the next boot can import using its unique host ID without force.
func reboot(ctx context.Context) error {
	if err := run(ctx, "systemctl", "stop", "floatlab-core-stack.service", "docker.service", "docker.socket", "floatlab-hostd.service"); err != nil {
		return err
	}
	if err := run(ctx, "zpool", "export", "floatlab"); err != nil {
		return fmt.Errorf("cannot safely export storage before reboot: %w", err)
	}
	return run(ctx, "systemctl", "reboot")
}

func workerActive(ctx context.Context) bool {
	out, err := Output(ctx, "systemctl", "show", "-p", "ActiveState", "--value", "floatlab-install-worker.service")
	state := strings.TrimSpace(string(out))
	return err == nil && (state == "active" || state == "activating")
}

func startWorker(ctx context.Context) error {
	state, err := Output(ctx, "systemctl", "show", "-p", "ActiveState", "--value", "floatlab-install-worker.service")
	if err != nil {
		return err
	}
	// Never reset a unit that has not run: systemd may have garbage-collected its
	// loaded state, and reset-failed then errors even though its unit file exists.
	if strings.TrimSpace(string(state)) == "failed" {
		if err = run(ctx, "systemctl", "reset-failed", "floatlab-install-worker.service"); err != nil {
			return err
		}
	}
	return run(ctx, "systemctl", "start", "--no-block", "floatlab-install-worker.service")
}
