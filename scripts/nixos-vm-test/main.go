// Command nixos-vm-test runs the appliance's NixOS VM checks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/floatlab/floatlab-core/internal/qemu"
)

var errVMExited = errors.New("VM exited")

type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	err  error
}

func start(cmd *exec.Cmd) (*process, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &process{cmd: cmd, done: make(chan struct{})}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *process) stop() {
	_ = p.cmd.Process.Kill()
	<-p.done
}

func retry(ctx context.Context, timeout, interval time.Duration, action func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		err := action(ctx)
		if err == nil || errors.Is(err, errVMExited) {
			return err
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

type vm struct {
	name, launcher, dir, logDir string
	env                         []string
	process                     *process
	boots                       int
	log                         string
}

func (v *vm) socket() string { return filepath.Join(v.dir, "agent.sock") }

func (v *vm) boot(ctx context.Context) error {
	if err := os.MkdirAll(v.dir, 0700); err != nil {
		return err
	}
	if err := os.Remove(v.socket()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	v.boots++
	v.log = filepath.Join(v.logDir, fmt.Sprintf("%s-boot-%d.log", v.name, v.boots))
	log, err := os.Create(v.log)
	if err != nil {
		return err
	}
	defer log.Close()
	fmt.Printf("Booting %s (boot %d)\n", v.name, v.boots)
	cmd := exec.CommandContext(ctx, v.launcher, "-display", "none", "-monitor", "none", "-no-reboot",
		"-serial", "stdio", "-device", "virtio-serial",
		"-chardev", "socket,id=qga,path="+v.socket()+",server=on,wait=off",
		"-device", "virtserialport,chardev=qga,name=org.qemu.guest_agent.0")
	cmd.Dir = v.dir
	cmd.Env = append(os.Environ(), "TMPDIR="+v.dir, "USE_TMPDIR=1", "NIX_DISK_IMAGE="+filepath.Join(v.dir, "root.qcow2"),
		"QEMU_OPTS=", "QEMU_NET_OPTS=", "QEMU_KERNEL_PARAMS=", "SHARED_DIR=")
	cmd.Env = append(cmd.Env, v.env...)
	cmd.Stdout, cmd.Stderr = log, log
	v.process, err = start(cmd)
	if err != nil {
		return err
	}
	return v.wait(ctx, 4*time.Minute, "true")
}

func (v *vm) reboot(ctx context.Context) error {
	if _, err := v.run(ctx, "systemd-run --on-active=2s systemctl poweroff"); err != nil {
		return err
	}
	ctxStop, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	select {
	case <-v.process.done:
		if v.process.err != nil {
			return fmt.Errorf("%s shutdown: %w", v.name, v.process.err)
		}
	case <-ctxStop.Done():
		return fmt.Errorf("%s shutdown: %w", v.name, ctxStop.Err())
	}
	return v.boot(ctx)
}

func (v *vm) run(ctx context.Context, command string) (string, error) {
	select {
	case <-v.process.done:
		return "", fmt.Errorf("%s: %w (%v)", v.name, errVMExited, v.process.err)
	default:
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := qemu.Dial(ctx, v.socket())
	if err != nil {
		return "", err
	}
	defer client.Close()
	script := `export PATH="/run/current-system/sw/bin:$PATH"; ` + command
	out, err := client.GuestExec(ctx, "/run/current-system/sw/bin/bash", []string{"-ec", script}, 100*time.Millisecond)
	if err != nil {
		return "", fmt.Errorf("%s: %s: %w", v.name, command, err)
	}
	return out, nil
}

func (v *vm) wait(ctx context.Context, timeout time.Duration, command string) error {
	return retry(ctx, timeout, time.Second, func(ctx context.Context) error {
		_, err := v.run(ctx, command)
		return err
	})
}

func (v *vm) close(failed bool) {
	if v.process != nil {
		v.process.stop()
	}
	if failed && v.log != "" {
		log, err := os.Open(v.log)
		if err == nil {
			defer log.Close()
			if size, err := log.Seek(0, io.SeekEnd); err == nil {
				_, _ = log.Seek(max(0, size-8192), io.SeekStart)
				fmt.Fprintf(os.Stderr, "\n%s console (%s)\n", v.name, v.log)
				_, _ = io.Copy(os.Stderr, log)
			}
		}
	}
}

func run(ctx context.Context, args []string) (err error) {
	if len(args) == 0 || (args[0] != "network" && args[0] != "recovery") {
		return errors.New("usage: nixos-vm-test {network|recovery} [--logs DIR] HOST [PEER]")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	logs := flags.String("logs", "", "directory for VM console logs")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	network := args[0] == "network"
	if (!network && flags.NArg() != 1) || (network && flags.NArg() != 2) {
		return errors.New("network requires host and peer launchers, recovery requires one launcher")
	}
	work, err := os.MkdirTemp("", "floatlab-vm-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	logDir := work
	if *logs != "" {
		logDir, err = filepath.Abs(*logs)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(logDir, 0700); err != nil {
			return err
		}
	}
	host := &vm{name: "host", launcher: flags.Arg(0), dir: filepath.Join(work, "host"), logDir: logDir}
	if !network {
		defer func() { host.close(err != nil) }()
		return testRecovery(ctx, host)
	}
	peer := &vm{name: "peer", launcher: flags.Arg(1), dir: filepath.Join(work, "peer"), logDir: logDir}
	for link := 1; link <= 3; link++ {
		socket := filepath.Join(work, fmt.Sprintf("lan%d", link))
		stop, err := startSwitch(ctx, socket)
		if err != nil {
			return err
		}
		defer stop()
		env := fmt.Sprintf("FLOATLAB_LINK_%d=%s", link, socket)
		host.env = append(host.env, env)
		peer.env = append(peer.env, env)
	}
	defer func() { host.close(err != nil); peer.close(err != nil) }()
	return testNetwork(ctx, host, peer)
}

func startSwitch(ctx context.Context, socket string) (func(), error) {
	cmd := exec.CommandContext(ctx, "vde_switch", "--sock", socket, "--dirmode", "0700", "--hub")
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	p, err := start(cmd)
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stop := func() {
		_ = stdin.Close()
		p.stop()
	}
	err = retry(ctx, 5*time.Second, 50*time.Millisecond, func(context.Context) error {
		select {
		case <-p.done:
			return fmt.Errorf("vde_switch: %w (%v)", errVMExited, p.err)
		default:
			_, err := os.Stat(filepath.Join(socket, "ctl"))
			return err
		}
	})
	if err != nil {
		stop()
		return nil, err
	}
	return stop, nil
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
