//go:build linux || darwin || freebsd

// Command installer-vm-test exercises the appliance installer in disposable QEMU VMs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/containerd/console"
	"github.com/floatlab/floatlab-core/internal/qemu"
	"github.com/google/shlex"
	"golang.org/x/sys/unix"
)

const (
	promptTimeout        = 20 * time.Minute
	initialPromptTimeout = 10 * time.Minute
	down                 = "\x1b[B"
	left                 = "\x1b[D"
	right                = "\x1b[C"
	username             = "installer-test"
	password             = "installer-test-password"
	consolePassword      = "console-recovery-password"
)

type options struct {
	iso, firmware, timezone, cli                string
	keep, static, totp, failures, beforeStorage bool
	disks                                       int
}

type scenario struct {
	options
	ctx                                     context.Context
	directory, socket, base, name, hostPort string
	disks                                   []string
}

func main() {
	var args options
	flag.StringVar(&args.iso, "iso", "", "installer ISO (required)")
	flag.StringVar(&args.firmware, "firmware", os.Getenv("FLOATLAB_OVMF"), "OVMF firmware directory")
	flag.StringVar(&args.timezone, "timezone", "UTC", "configured timezone")
	flag.StringVar(&args.cli, "cli", "", "compiled FloatLab CLI")
	flag.BoolVar(&args.keep, "keep", false, "keep VM files on success")
	flag.BoolVar(&args.static, "static", false, "install and verify static IPv4")
	flag.BoolVar(&args.totp, "totp", false, "verify TOTP and one-time recovery codes")
	flag.BoolVar(&args.failures, "failures", false, "inject a bootloader failure")
	flag.BoolVar(&args.beforeStorage, "before-storage", false, "interrupt at the first wipe command")
	flag.IntVar(&args.disks, "disks", 0, "disk count (1 or 2, defaults to both layouts)")
	flag.Parse()
	if args.iso == "" || args.firmware == "" || args.disks < 0 || args.disks > 2 || (args.failures && args.beforeStorage) || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	counts := []int{1, 2}
	if args.disks != 0 {
		counts = []int{args.disks}
	}
	for _, count := range counts {
		if err := args.run(ctx, count); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func allocatePort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return fmt.Sprint(listener.Addr().(*net.TCPAddr).Port), nil
}

func copyFile(from, to string) error {
	source, err := os.Open(from)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(target, source)
	return errors.Join(err, target.Close())
}

func (options options) run(ctx context.Context, count int) (result error) {
	path, err := filepath.Abs(options.iso)
	if err != nil {
		return err
	}
	options.iso = path
	for _, path := range []string{options.iso, filepath.Join(options.firmware, "OVMF_CODE.fd"), filepath.Join(options.firmware, "OVMF_VARS.fd")} {
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	directory, err := os.MkdirTemp("", "floatlab-installer-vm-")
	if err != nil {
		return err
	}
	fmt.Println("Installer VM logs:", directory)
	defer func() {
		if result == nil && !options.keep {
			result = os.RemoveAll(directory)
		}
	}()
	sockets, err := os.MkdirTemp("/tmp", "floatlab-qga-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(sockets)
	port, err := allocatePort()
	if err != nil {
		return err
	}
	test := scenario{options: options, ctx: ctx, directory: directory, socket: filepath.Join(sockets, "guest.sock"),
		base: "http://127.0.0.1:" + port, hostPort: port, name: fmt.Sprintf("installed-%d", count)}
	for index := range count {
		path := filepath.Join(directory, fmt.Sprintf("disk-%d.qcow2", index))
		if out, err := exec.CommandContext(ctx, "qemu-img", "create", "-f", "qcow2", path, "32G").CombinedOutput(); err != nil {
			return fmt.Errorf("create disk: %w: %s", err, out)
		}
		test.disks = append(test.disks, path)
	}
	if err := copyFile(filepath.Join(options.firmware, "OVMF_VARS.fd"), filepath.Join(directory, "vars.fd")); err != nil {
		return err
	}
	all := make([]int, count)
	for i := range count {
		all[i] = i
	}
	node, err := test.install(all)
	if err != nil {
		return err
	}
	if options.beforeStorage || options.failures {
		return nil
	}
	sets := [][]int{all}
	if count > 1 {
		for i := range count {
			sets = append(sets, []int{i})
		}
	}
	for _, selected := range sets {
		for iteration := range 2 {
			if err := test.boot(node, selected, iteration); err != nil {
				return err
			}
		}
	}
	fmt.Printf("PASS: %d-disk console installation and disk-only boots\n", count)
	return nil
}

func (s scenario) spawn(selected []int, install bool, label string) (*terminal, func(), error) {
	accel := "tcg"
	if unix.Access("/dev/kvm", unix.R_OK|unix.W_OK) == nil {
		accel = "kvm"
	}
	host := ""
	if s.static {
		host = "10.0.2.20"
	}
	if err := os.Remove(s.socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	args := []string{"-machine", "q35", "-m", "6144", "-smp", "4", "-accel", accel,
		"-drive", "if=pflash,format=raw,readonly=on,file=" + filepath.Join(s.firmware, "OVMF_CODE.fd"),
		"-drive", "if=pflash,format=raw,file=" + filepath.Join(s.directory, "vars.fd"),
		"-display", "none", "-serial", "stdio", "-no-reboot",
		"-netdev", "user,id=lan,restrict=on,hostfwd=tcp:127.0.0.1:" + s.hostPort + "-" + host + ":8080",
		"-device", "virtio-net-pci,netdev=lan", "-device", "virtio-serial",
		"-chardev", "socket,path=" + s.socket + ",server=on,wait=off,id=agent",
		"-device", "virtserialport,chardev=agent,name=org.qemu.guest_agent.0"}
	for _, index := range selected {
		args = append(args, "-drive", fmt.Sprintf("if=none,id=disk%d,format=qcow2,file=%s", index, s.disks[index]),
			"-device", fmt.Sprintf("virtio-blk-pci,drive=disk%d,serial=floatlab-installer-%d", index, index))
	}
	if install {
		args = append(args, "-cdrom", s.iso, "-boot", "order=d")
	} else {
		args = append(args, "-boot", "order=c")
	}
	log, err := os.OpenFile(filepath.Join(s.directory, label+".log"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, nil, err
	}
	vm, err := startTerminal(exec.CommandContext(s.ctx, "qemu-system-x86_64", args...), console.WinSize{Height: 40, Width: 140}, log)
	if err != nil {
		log.Close()
		return nil, nil, err
	}
	return vm, func() { vm.close(); log.Close() }, nil
}

func (s scenario) guest(command string) (string, error) {
	ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
	defer cancel()
	client, err := qemu.Dial(ctx, s.socket)
	if err != nil {
		return "", err
	}
	defer client.Close()
	script := `set -e; export PATH="/run/current-system/sw/bin:$PATH"; ` + command
	return client.GuestExec(ctx, "/run/current-system/sw/bin/sh", []string{"-c", script}, time.Second)
}

func (s scenario) waitGuest(vm *terminal, command string) error {
	deadline := time.Now().Add(10 * time.Minute)
	var err error
	for time.Now().Before(deadline) {
		if _, err = s.guest(command); err == nil {
			return nil
		}
		select {
		case <-vm.done:
			return fmt.Errorf("VM exited waiting for %s: %v", command, err)
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("guest condition timed out: %s: %w", command, err)
}

func quote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func (s scenario) injectFailure() (string, error) {
	env, err := s.guest("systemctl show -p Environment --value floatlab-installer-console@ttyS0")
	if err != nil {
		return "", err
	}
	values, err := shlex.Split(env)
	if err != nil {
		return "", err
	}
	var workerPath string
	for _, value := range values {
		if path, found := strings.CutPrefix(value, "PATH="); found {
			workerPath = path
			break
		}
	}
	if workerPath == "" {
		return "", errors.New("missing installer console PATH")
	}
	tool, script := "zbm-kcl", "#!/run/current-system/sw/bin/sh\necho Injected bootloader failure >&2\nexit 1\n"
	if s.beforeStorage {
		tool, script = "wipefs", "#!/run/current-system/sw/bin/sh\ntouch /run/floatlab-installer-test/wipefs-entered\nsleep 600\n"
	}
	const dir = "/run/systemd/system/floatlab-installer-console@ttyS0.service.d"
	_, err = s.guest("mkdir -p /run/floatlab-installer-test " + dir + "; printf '%s' " + quote(script) + " > /run/floatlab-installer-test/" + tool +
		"; chmod 700 /run/floatlab-installer-test/" + tool + "; printf '%s\\n' '[Service]' " + quote(`Environment="PATH=/run/floatlab-installer-test:`+workerPath+`"`) +
		" > " + dir + "/interruption.conf; systemctl daemon-reload; systemctl restart floatlab-installer-console@ttyS0")
	if err != nil {
		return "", err
	}
	return s.guest("lsblk -nro PARTUUID | sort")
}
