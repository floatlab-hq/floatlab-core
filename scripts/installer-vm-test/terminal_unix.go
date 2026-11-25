//go:build linux || darwin || freebsd

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/console"
)

type terminal struct {
	master                 console.Console
	command                *exec.Cmd
	done, changed, drained chan struct{}
	mu                     sync.Mutex
	output                 []byte
	exit                   error
}

func startTerminal(cmd *exec.Cmd, size console.WinSize, log io.Writer) (*terminal, error) {
	pty, path, err := console.NewPty()
	if err != nil {
		return nil, err
	}
	if err := pty.Resize(size); err != nil {
		pty.Close()
		return nil, err
	}
	slave, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		pty.Close()
		return nil, err
	}
	defer slave.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		pty.Close()
		return nil, err
	}
	p := &terminal{master: pty, command: cmd, done: make(chan struct{}), changed: make(chan struct{}, 1), drained: make(chan struct{})}
	go func() {
		p.exit = cmd.Wait()
		close(p.done)
	}()
	go func() {
		defer close(p.drained)
		buf := make([]byte, 32768)
		for {
			n, err := pty.Read(buf)
			if n > 0 {
				if log != nil {
					_, _ = log.Write(buf[:n])
				}
				p.mu.Lock()
				p.output = append(p.output, buf[:n]...)
				// shortcut: retain the last MiB for matching, enlarge if prompts exceed it.
				if len(p.output) > 1<<20 {
					p.output = append([]byte(nil), p.output[len(p.output)-(1<<20):]...)
				}
				p.mu.Unlock()
				select {
				case p.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return p, nil
}

func (p *terminal) send(input string) error {
	_, err := io.WriteString(p.master, input)
	return err
}

func (p *terminal) expect(pattern string, timeout time.Duration) ([]string, error) {
	expression, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	drained := false
	for {
		p.mu.Lock()
		indices := expression.FindSubmatchIndex(p.output)
		if indices != nil {
			groups := make([]string, len(indices)/2)
			for i := range groups {
				if a, b := indices[i*2], indices[i*2+1]; a >= 0 {
					groups[i] = string(p.output[a:b])
				}
			}
			p.output = p.output[indices[1]:]
			p.mu.Unlock()
			return groups, nil
		}
		p.mu.Unlock()
		if drained {
			return nil, fmt.Errorf("terminal closed waiting for %q: %w", pattern, io.EOF)
		}
		select {
		case <-p.drained:
			drained = true
		case <-p.changed:
		case <-timer.C:
			return nil, fmt.Errorf("timed out waiting for %q", pattern)
		}
	}
}

func (p *terminal) wait(timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-p.done:
		return p.exit
	case <-timer.C:
		return errors.New("timed out waiting for terminal process to exit")
	}
}

func (p *terminal) close() {
	_ = p.command.Process.Kill()
	<-p.done
	p.master.Close()
	<-p.drained
}
