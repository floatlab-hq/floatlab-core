package installer

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// One JSON object per line in both directions. stdout contains protocol only.
type Request struct {
	ID           int            `json:"id"`
	Type         string         `json:"type"`
	Command      string         `json:"command,omitempty"`
	Confirmation string         `json:"confirmation,omitempty"`
	Config       *InstallConfig `json:"config,omitempty"`
}
type InstallConfig struct {
	NodeName             string             `json:"node_name"`
	Timezone             string             `json:"timezone"`
	Network              hostnetwork.Config `json:"network"`
	Username             string             `json:"username"`
	Password             string             `json:"password"`
	PasswordConfirmation string             `json:"password_confirmation"`
	RecoveryPassword     string             `json:"recovery_password"`
	RecoveryConfirmation string             `json:"recovery_password_confirmation"`
	TOTP                 bool               `json:"totp"`
	TOTPCode             string             `json:"totp_code,omitempty"`
	RecoverySaved        bool               `json:"recovery_saved,omitempty"`
}
type Enrollment struct {
	URI           string   `json:"uri"`
	Secret        string   `json:"secret"`
	RecoveryCodes []string `json:"recovery_codes"`
}
type Event struct {
	ID           int         `json:"id,omitempty"`
	Type         string      `json:"type"`
	Status       *Status     `json:"status,omitempty"`
	Disks        []Disk      `json:"disks,omitempty"`
	PayloadBytes int64       `json:"payload_bytes,omitempty"`
	Command      string      `json:"command,omitempty"`
	DryRun       string      `json:"dry_run,omitempty"`
	NTP          bool        `json:"ntp"`
	Enrollment   *Enrollment `json:"enrollment,omitempty"`
	Error        string      `json:"error,omitempty"`
}
type layout struct {
	disks   []Disk
	command string
	dryRun  string
}

// Service runs the same session state machine for the real and mock backends.
type Service struct {
	Mock             bool
	Scenario         string
	StepDelay        time.Duration
	runtimeDir       string
	persistentDir    string
	mediaDir         string
	inventory        func(context.Context) ([]Disk, error)
	previewPool      func(context.Context, []string) (string, error)
	reboot           func(context.Context) error
	disks            []Disk
	payloadBytes     int64
	identity         Appliance
	key              []byte
	ntp              bool
	enrollment       *Enrollment
	recoveryHashes   []string
	verifiedTOTPCode string
}

// Inventory is shared between consoles; the first preview claims the session.
func (s *Service) claimSession() (*os.File, error) {
	lock, err := Lock(s.runtimeDir + "/session.lock")
	if err != nil {
		return nil, err
	}
	for _, path := range []string{s.runtimeDir + "/operation.json", s.runtimeDir + "/status.json", s.persistentDir + "/install-operation.json"} {
		_, err = os.Stat(path)
		if err == nil {
			err = fmt.Errorf("an installation has already started; restart the console to inspect its status")
		}
		if !os.IsNotExist(err) {
			lock.Close()
			return nil, err
		}
	}
	return lock, nil
}

func (s *Service) Serve(parent context.Context, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var mu sync.Mutex
	encoder := json.NewEncoder(output)
	send := func(e Event) {
		mu.Lock()
		defer mu.Unlock()
		if encoder.Encode(e) != nil {
			cancel()
		}
	}
	if s.runtimeDir == "" {
		s.runtimeDir, s.persistentDir, s.mediaDir = Runtime, Persistent, "/iso"
	}
	if s.Mock {
		if s.StepDelay <= 0 {
			s.StepDelay = 250 * time.Millisecond
		}
		switch s.Scenario {
		case "", "success", "delayed-config", "rejected-config", "failure", "cancellation":
		default:
			return fmt.Errorf("unknown mock scenario %q", s.Scenario)
		}
		s.setupMock()
	} else {
		if s.inventory == nil {
			s.inventory = Inventory
		}
		if s.previewPool == nil {
			s.previewPool = dryRunPool
		}
		if s.reboot == nil {
			s.reboot = reboot
		}
		var previous Status
		err := credentials.ReadPrivate(s.runtimeDir+"/status.json", &previous)
		if err != nil && !os.IsNotExist(err) {
			return s.terminal(ctx, input, send, Status{Phase: "Installer startup", Error: Diagnostic([]byte(err.Error()))})
		}
		if previous.Phase != "" {
			if !previous.Ready && previous.Error == "" {
				previous.Error = "Installation interrupted; inspect the partial installation before reinstalling"
			}
			return s.terminal(ctx, input, send, previous)
		}
		if _, err = os.Stat(s.persistentDir + "/install-operation.json"); err == nil {
			return s.terminal(ctx, input, send, Status{Phase: "Installation interrupted", Error: "A partial installation exists; inspect it before reinstalling"})
		} else if !os.IsNotExist(err) {
			return s.terminal(ctx, input, send, Status{Phase: "Installer startup", Error: Diagnostic([]byte(err.Error()))})
		}
		if err = checkFirmware(); err != nil {
			return s.terminal(ctx, input, send, Status{Phase: "Installer startup", Error: Diagnostic([]byte(err.Error()))})
		}
		var payload Payload
		data, err := os.ReadFile(s.mediaDir + "/floatlab-manifest.json")
		if err == nil {
			err = json.Unmarshal(data, &payload)
		}
		if err == nil {
			err = payload.Validate()
		}
		if err != nil {
			return s.terminal(ctx, input, send, Status{Phase: "Installer startup", Error: Diagnostic([]byte(err.Error()))})
		}
		s.payloadBytes = payload.RootBytes
		s.disks, err = s.inventory(ctx)
		if err != nil {
			return s.terminal(ctx, input, send, Status{Phase: "Installer startup", Error: Diagnostic([]byte(err.Error()))})
		}
		synced, _ := Output(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
		s.ntp = strings.TrimSpace(string(synced)) == "yes"
	}
	hostID, err := credentials.Random(4)
	if err != nil {
		return err
	}
	if hex.EncodeToString(hostID) == "00000000" {
		hostID[0] = 1
	}
	jwt, err := credentials.Random(32)
	if err != nil {
		return err
	}
	s.key, err = credentials.Random(32)
	if err != nil {
		return err
	}
	s.identity = Appliance{NodeID: uuid.NewString(), HostID: hex.EncodeToString(hostID), JWTSecret: hex.EncodeToString(jwt)}
	status := Status{Phase: "Waiting for disk layout", Activity: "Select disks and confirm the installation layout"}
	send(Event{Type: "inventory", Disks: s.disks, PayloadBytes: s.payloadBytes, NTP: s.ntp, Status: &status})
	requests := readRequests(ctx, input)
	var sessionLock *os.File
	defer func() {
		if sessionLock != nil {
			sessionLock.Close()
		}
	}()
	var planned *layout
	var done chan error
	started, accepted, terminal := false, false, false
	rejectedOnce := false
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	workerStatus := make(chan Status, 64)
	configured := make(chan struct{})
	// Wait for an active operation to finish and clean up on EOF or signal.
	defer func() {
		cancel()
		if done != nil {
			<-done
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case st := <-workerStatus:
			status = st
			send(Event{Type: "status", Status: &status})
		case workerErr := <-done:
			done = nil
			for len(workerStatus) > 0 {
				status = <-workerStatus
			}
			terminal = true
			if workerErr != nil {
				status.Error = Diagnostic([]byte(workerErr.Error()))
				send(Event{Type: "failed", Status: &status})
			} else {
				status.Ready = true
				status.Progress = 1
				status.Phase = "Installation complete"
				send(Event{Type: "complete", Status: &status})
			}
		case item, ok := <-requests:
			if !ok {
				return io.EOF
			}
			req := item.request
			reply := Event{ID: req.ID, Type: req.Type}
			if item.err != nil {
				reply.Type = "error"
				reply.Error = "Invalid protocol message"
				send(reply)
				continue
			}
			fail := func(err error) { reply.Type = "error"; reply.Error = validationText(err.Error()); send(reply) }
			switch req.Type {
			case "preview":
				if !s.Mock && sessionLock == nil {
					var err error
					sessionLock, err = s.claimSession()
					if err != nil {
						fail(err)
						continue
					}
				}
				if started {
					fail(fmt.Errorf("disk layout is already confirmed"))
					continue
				}
				planned = nil
				args, e := ParseZPool(req.Command, s.disks)
				if e != nil {
					fail(e)
					continue
				}
				selected := commandDisks(args, s.disks)
				if e = ValidateSelection(selected, s.payloadBytes); e != nil {
					fail(e)
					continue
				}
				dry, e := s.previewPool(ctx, wholeDiskArgs(args, selected))
				if e != nil {
					fail(e)
					continue
				}
				planned = &layout{selected, "zpool " + strings.Join(args, " "), dry}
				reply.Disks = selected
				reply.Command = planned.command
				reply.DryRun = dry
				send(reply)
			case "confirm":
				if started || planned == nil || req.Confirmation != "ERASE ALL DATA" || req.Command != planned.command {
					fail(fmt.Errorf("preview the command and type ERASE ALL DATA to confirm"))
					continue
				}
				current, e := s.inventory(ctx)
				if e == nil {
					e = Revalidate(planned.disks, current)
				}
				if e != nil {
					planned = nil
					fail(e)
					continue
				}
				if !s.Mock {
					if e = credentials.WritePrivate(s.runtimeDir+"/operation.json", Operation{Disks: planned.disks, Command: planned.command, Identity: s.identity}); e == nil {
						e = credentials.WriteBytes(s.runtimeDir+"/credential.key", s.key)
					}
					if e != nil {
						fail(e)
						continue
					}
				}
				started = true
				status.Started = time.Now()
				done = make(chan error, 1)
				send(reply)
				emit := func(st Status) {
					select {
					case workerStatus <- st:
					case <-workerCtx.Done():
					}
				}
				go func() {
					var e error
					if s.Mock {
						e = s.mockInstall(workerCtx, planned.disks, configured, emit)
					} else {
						e = installWorker(workerCtx, s.runtimeDir, s.persistentDir, s.mediaDir, s.inventory, emit)
					}
					done <- e
				}()
			case "validate-host":
				if !started || terminal || accepted || req.Config == nil {
					fail(fmt.Errorf("host settings are unavailable in the current installation state"))
					continue
				}
				if _, e := s.hostSettings(*req.Config); e != nil {
					fail(e)
					continue
				}
				send(reply)
			case "validate-account", "verify-totp":
				if !started || terminal || accepted || req.Config == nil {
					fail(fmt.Errorf("account settings are unavailable in the current installation state"))
					continue
				}
				if e := validateAccount(*req.Config); e != nil {
					fail(e)
					continue
				}
				if req.Type == "verify-totp" {
					if s.enrollment == nil {
						fail(fmt.Errorf("enroll your authenticator first"))
						continue
					}
					if _, ok := credentials.Verify(s.enrollment.Secret, req.Config.TOTPCode, time.Now(), -1); !ok {
						fail(fmt.Errorf("invalid or expired authenticator code"))
						continue
					}
					s.verifiedTOTPCode = req.Config.TOTPCode
				}
				send(reply)

			case "enroll", "configure":
				if !started || terminal || accepted || req.Config == nil {
					fail(fmt.Errorf("configuration is unavailable in the current installation state"))
					continue
				}
				if s.Mock && s.Scenario == "rejected-config" && !rejectedOnce && req.Type == "configure" {
					rejectedOnce = true
					fail(fmt.Errorf("Mock: configuration rejected; correct or resubmit the form"))
					continue
				}
				sub, e := s.submission(ctx, *req.Config, req.Type == "enroll")
				if e != nil {
					fail(e)
					continue
				}
				if req.Type == "enroll" {
					reply.Enrollment = s.enrollment
					send(reply)
					continue
				}
				if !s.Mock {
					e = credentials.WritePrivate(s.runtimeDir+"/submission.json", sub)
				}
				if e != nil {
					fail(e)
					continue
				}
				accepted = true
				close(configured)
				s.enrollment = nil
				s.recoveryHashes = nil
				s.verifiedTOTPCode = ""
				send(reply)
			case "reboot":
				if done != nil {
					stopWorker()
					<-done
					done = nil
					terminal = true
				}
				if e := s.reboot(ctx); e != nil {
					fail(e)
					continue
				}
				send(reply)
				return nil
			default:
				fail(fmt.Errorf("unknown request type"))
			}
		}
	}
}

type requestResult struct {
	request Request
	err     error
}

func readRequests(ctx context.Context, input io.Reader) <-chan requestResult {
	ch := make(chan requestResult)
	go func() {
		defer close(ch)
		scan := bufio.NewScanner(input)
		scan.Buffer(make([]byte, 4096), 1<<20)
		for scan.Scan() {
			var req Request
			err := json.Unmarshal(scan.Bytes(), &req)
			select {
			case ch <- requestResult{req, err}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}

func (s *Service) terminal(ctx context.Context, input io.Reader, send func(Event), status Status) error {
	kind := "failed"
	if status.Ready {
		kind = "complete"
	}
	send(Event{Type: kind, Status: &status})
	requests := readRequests(ctx, input)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case item, ok := <-requests:
			if !ok {
				return io.EOF
			}
			if item.err == nil && item.request.Type == "reboot" {
				if err := s.reboot(ctx); err != nil {
					send(Event{ID: item.request.ID, Type: "error", Error: Diagnostic([]byte(err.Error()))})
					continue
				}
				send(Event{ID: item.request.ID, Type: "reboot"})
				return nil
			}
			send(Event{ID: item.request.ID, Type: "error", Error: "Installation is finished; reboot is the only available operation"})
		}
	}
}

func checkFirmware() error {
	if _, err := os.Stat("/sys/firmware/efi"); err != nil {
		return fmt.Errorf("boot the installation media in UEFI mode")
	}
	entries, err := os.ReadDir("/sys/firmware/efi/efivars")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "SecureBoot-") {
			data, err := os.ReadFile("/sys/firmware/efi/efivars/" + entry.Name())
			if err != nil {
				return err
			}
			if len(data) > 4 && data[4] != 0 {
				return fmt.Errorf("disable Secure Boot before installing")
			}
		}
	}
	return nil
}

func validateAccount(config InstallConfig) error {
	if config.Username == "" {
		return fmt.Errorf("enter an administrator username")
	}
	if config.Password == "" {
		return fmt.Errorf("enter a password")
	}
	if config.PasswordConfirmation == "" {
		return fmt.Errorf("confirm your password")
	}
	if config.Password != config.PasswordConfirmation {
		return fmt.Errorf("passwords do not match")
	}
	if !credentials.Valid(config.Username, config.Password) {
		return fmt.Errorf("username must be 1–64 characters without line breaks; password must be 1–72 bytes")
	}
	if !credentials.Valid("root", config.RecoveryPassword) || strings.ContainsAny(config.RecoveryPassword, "\x00\r\n") {
		return fmt.Errorf("console recovery password must be 1–72 bytes without NUL or line breaks")
	}
	if config.RecoveryPassword != config.RecoveryConfirmation {
		return fmt.Errorf("console recovery passwords do not match")
	}
	return nil
}

func (s *Service) submission(ctx context.Context, config InstallConfig, enroll bool) (Submission, error) {
	var sub Submission
	host, err := s.hostSettings(config)
	if err != nil {
		return sub, err
	}
	sub.Host = host
	if err := validateAccount(config); err != nil {
		return sub, err
	}
	if enroll || config.TOTP {
		if !s.Mock {
			synced, _ := Output(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value")
			s.ntp = strings.TrimSpace(string(synced)) == "yes"
		}
		if !s.ntp {
			return sub, fmt.Errorf("NTP must be synchronized for TOTP enrollment; use authentication without TOTP or try again")
		}
		if enroll {
			s.verifiedTOTPCode = ""
			secret, err := credentials.Secret()
			if err != nil {
				return sub, err
			}
			codes, hashes, err := credentials.RecoveryCodes()
			if err != nil {
				return sub, err
			}
			s.enrollment = &Enrollment{URI: credentials.URI(secret, config.Username, config.NodeName), Secret: secret, RecoveryCodes: codes}
			s.recoveryHashes = hashes
			return sub, nil
		}
		if s.enrollment == nil || !config.RecoverySaved {
			return sub, fmt.Errorf("enroll TOTP and confirm the recovery codes have been saved")
		}
		if s.verifiedTOTPCode == "" || s.verifiedTOTPCode != config.TOTPCode {
			if _, ok := credentials.Verify(s.enrollment.Secret, config.TOTPCode, time.Now(), -1); !ok {
				return sub, fmt.Errorf("invalid or expired authenticator code")
			}
		}
		secret, err := credentials.Encrypt(s.key, s.enrollment.Secret)
		if err != nil {
			return sub, err
		}
		sub.Admin.TOTPSecret, sub.Admin.RecoveryHashes = secret, s.recoveryHashes
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(config.Password), bcrypt.DefaultCost)
	if err != nil {
		return sub, err
	}
	sub.Admin.Username, sub.Admin.PasswordHash = config.Username, string(hash)
	sub.Admin.NodeID, sub.Admin.NodeName = sub.Host.NodeID, sub.Host.NodeName
	recoveryHash, err := bcrypt.GenerateFromPassword([]byte(config.RecoveryPassword), bcrypt.DefaultCost)
	if err != nil {
		return sub, err
	}
	sub.RecoveryPasswordHash = string(recoveryHash)
	sub.VerificationPassword, err = credentials.Encrypt(s.key, config.Password)
	return sub, err
}

func (s *Service) setupMock() {
	s.payloadBytes = 4 * GiB
	s.ntp = true
	s.disks = []Disk{
		{Path: "/dev/disk/by-id/mock-a", Device: "/dev/sda", Manufacturer: "Samsung", Model: "Mock SSD", Size: 512 * GiB, Eligible: true, Fingerprint: "a", Partitions: []Partition{{492 * GiB, "NTFS"}}},
		{Path: "/dev/disk/by-id/mock-b", Device: "/dev/sdb", Manufacturer: "Seagate", Model: "Mock HDD", Size: 2 * 1024 * GiB, Eligible: true, Fingerprint: "b", Partitions: []Partition{{8 * GiB, "ext4"}, {1981 * GiB, "ext4"}}},
		{Path: "/dev/disk/by-id/mock-media", Device: "/dev/sdc", Manufacturer: "FloatLab", Model: "Installation media", Size: 16 * GiB, Eligible: false, Reason: "Installation media"},
	}
	s.inventory = func(context.Context) ([]Disk, error) { return s.disks, nil }
	s.previewPool = func(context.Context, []string) (string, error) { return "Mock dry-run: pool layout is valid", nil }
	s.reboot = func(context.Context) error { return nil }
}
func (s *Service) mockInstall(ctx context.Context, disks []Disk, configured <-chan struct{}, emit func(Status)) error {
	st := Status{Started: time.Now()}
	wait := func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.StepDelay):
			return nil
		}
	}
	for _, phase := range []string{"Partition and format disks", "Create ZFS storage volume"} {
		st.Phase, st.Activity = phase, phase
		st.Progress += 0.05
		emit(st)
		if err := wait(); err != nil {
			return err
		}
	}
	st.PoolReady = true
	st.Phase = "Installing Floatlab"
	st.Total = s.payloadBytes
	for i := 1; i <= 40; i++ {
		if err := wait(); err != nil {
			return err
		}
		st.Copied = st.Total * int64(i) / 40
		st.Progress = 0.1 + 0.8*float64(i)/40
		st.BytesPerSecond = float64(st.Total/40) / s.StepDelay.Seconds()
		st.Activity = "Copying Floatlab OS into ZFS"
		emit(st)
		if s.Scenario == "failure" && i == 20 {
			return errors.New("Mock: image copy failed")
		}
		if s.Scenario == "cancellation" && i == 20 {
			return errors.New("Mock: frontend connection lost; active operation finished and cleanup completed")
		}
	}
	st.Phase, st.Activity = "Installing bootloader", "Installing ZFSBootMenu on each disk"
	emit(st)
	if err := wait(); err != nil {
		return err
	}
	st.StorageReady = true
	st.Phase, st.Activity = "Waiting for host settings and credentials", "Waiting for valid configuration"
	select {
	case <-configured:
	default:
		emit(st)
	}
	if s.Scenario == "delayed-config" {
		for range 8 {
			if err := wait(); err != nil {
				return err
			}
		}
	}
	select {
	case <-configured:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, phase := range []string{"Persist and apply host configuration", "Fetch images and verify FloatLab services"} {
		st.Phase, st.Activity = phase, phase
		st.Progress += 0.04
		emit(st)
		if err := wait(); err != nil {
			return err
		}
	}
	st.Ready = true
	st.Progress = 1
	st.Phase = "Installation complete"
	st.Activity = "Installation verified"
	st.Address = "http://192.0.2.10:8080"
	emit(st)
	return nil
}

// Validation errors contain no credential values. Remove terminal controls while
// keeping instructions about passwords and recovery codes readable.
func validationText(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, value)
	if len(value) > 2048 {
		value = value[:2048]
	}
	return value
}

func (s *Service) hostSettings(config InstallConfig) (Appliance, error) {
	if config.Timezone == "" {
		return Appliance{}, fmt.Errorf("please select a timezone")
	}
	host := s.identity
	host.NodeName, host.Timezone, host.Network = config.NodeName, config.Timezone, config.Network
	return host, host.Validate()
}
