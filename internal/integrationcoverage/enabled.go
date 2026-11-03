//go:build integrationcoverage

// Package integrationcoverage checkpoints only instrumented integration binaries.
package integrationcoverage

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/coverage"
	"strings"
	"sync"
	"syscall"
	"time"
)

var flush = func() {}

func Flush() { flush() }

// Sandbox coverage stays inside the existing disposable workload mount.
func ConfigureSandbox(service map[string]any) {
	service["environment"].(map[string]string)["GOCOVERDIR"] = "/var/lib/floatlab/workloads/integration-coverage"
}

func ArchiveSandbox(source, operation string) error {
	entries, err := os.ReadDir(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		target := filepath.Join("/floatlab/integration-coverage", "sandbox-"+operation, entry.Name())
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.CopyFS(target, os.DirFS(filepath.Join(source, entry.Name()))); err != nil {
			return err
		}
	}
	return nil
}

func init() {
	root := os.Getenv("GOCOVERDIR")
	if root == "" {
		if _, err := os.Stat("/floatlab/system/etc/installed"); err != nil {
			return
		}
		root = "/floatlab/integration-coverage"
	}
	if err := os.MkdirAll(root, 0777); err != nil {
		panic(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		panic(err)
	}
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		panic(err)
	}
	fields := strings.Fields(string(stat)[strings.LastIndex(string(stat), ")")+2:])
	dir := filepath.Join(root, fmt.Sprintf("%s-%s-%d-%s", strings.TrimSpace(string(boot)), filepath.Base(os.Args[0]), os.Getpid(), fields[19]))
	if err := os.MkdirAll(dir, 0777); err != nil {
		panic(err)
	}
	var mu sync.Mutex
	flush = func() {
		mu.Lock()
		defer mu.Unlock()
		if err := coverage.WriteMetaDir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		if err := coverage.WriteCountersDir(dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
		// The independent boot guard starts before persistent datasets mount.
		if root == "/run/floatlab-boot-recovery/coverage" {
			mounts, err := os.ReadFile("/proc/self/mountinfo")
			if err == nil && strings.Contains(string(mounts), " /floatlab/integration-coverage ") {
				target := filepath.Join("/floatlab/integration-coverage", filepath.Base(dir))
				if err := copyCheckpoint(dir, target); err != nil {
					fmt.Fprintln(os.Stderr, err)
					return
				}
			}
		}
		if err := os.WriteFile(filepath.Join(dir, "checkpoint"), []byte(time.Now().Format(time.RFC3339Nano)), 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR2)
	go func() {
		for range ch {
			flush()
		}
	}()
}

func copyCheckpoint(source, target string) error {
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), data, 0600); err != nil {
			return err
		}
	}
	return nil
}
