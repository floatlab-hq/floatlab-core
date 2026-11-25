//go:build linux || darwin || freebsd

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/credentials"
)

type installedNode struct {
	id, snapshot, secret string
	recovery             []string
}

func (p *terminal) prompt(pattern string) error {
	_, err := p.expect(pattern, promptTimeout)
	return err
}

func (p *terminal) reply(prompt, input string, timeout time.Duration) error {
	if _, err := p.expect(prompt, timeout); err != nil {
		return err
	}
	return p.send(input)
}

func (s scenario) install(disks []int) (node installedNode, result error) {
	vm, closeVM, err := s.spawn(disks, true, "install")
	if err != nil {
		return node, err
	}
	defer closeVM()
	defer func() {
		if result == nil {
			return
		}
		diagnostics, err := s.guest("journalctl -u floatlab-installer-console@ttyS0 -u floatlab-installer-root -u docker -u floatlab-hostd -u floatlab-core-stack -n 80 --no-pager; tail -n 20 /tmp/install-tui-*.log")
		if err != nil {
			fmt.Println("Guest diagnostics unavailable:", err)
		} else {
			fmt.Println(diagnostics)
		}
	}()
	if _, err := vm.expect("Select installation disks", initialPromptTimeout); err != nil {
		return node, err
	}
	if _, err := s.guest(`test ! -x /run/current-system/sw/bin/docker;
		test ! -x /run/current-system/sw/bin/docker-compose;
		test ! -x /run/current-system/sw/bin/git;
		test -x /run/current-system/sw/bin/nix-shell;
		test ! -e /run/floatlab-installer/root/run/docker.sock`); err != nil {
		return node, err
	}

	var partitions string
	if s.beforeStorage || s.failures {
		partitions, err = s.injectFailure()
		if err != nil {
			return node, err
		}
		if err := vm.prompt("Select installation disks"); err != nil {
			return node, err
		}
	}
	if err := vm.send(strings.Repeat(" "+down, len(disks)-1) + " \n"); err != nil {
		return node, err
	}
	if err := vm.reply("Erase All and continue", down+down+"\n", promptTimeout); err != nil {
		return node, err
	}
	if err := vm.prompt("Configure FloatLab while installation runs"); err != nil {
		return node, err
	}
	if s.beforeStorage || s.failures {
		return node, s.checkInterrupted(vm, len(disks), partitions)
	}
	if err := vm.send(fill(s.name) + "\n"); err != nil {
		return node, err
	}
	ipChoice := left
	network := ""
	if s.static {
		ipChoice = right
		network = fill("10.0.2.20") + fill("255.255.255.0") + fill("10.0.2.2")
	}
	if err := vm.reply("Choose timezone", s.timezone+"\n"+ipChoice+"\n"+network+fill("10.0.2.3"), promptTimeout); err != nil {
		return node, err
	}
	if err := vm.prompt("Set up administrator"); err != nil {
		return node, err
	}
	factorChoice := right
	if s.totp {
		factorChoice = left
		if err := s.waitGuest(vm, `test "$(timedatectl show -p NTPSynchronized --value)" = yes`); err != nil {
			return node, err
		}
	}
	// Accept the default SSH key source before selecting the second factor.
	if err := vm.send(fill(username) + fill(password) + fill(password) + fill(consolePassword) + fill(consolePassword) + "\n" + factorChoice + "\n"); err != nil {
		return node, err
	}
	if s.totp {
		if err := vm.prompt("Enroll your authenticator"); err != nil {
			return node, err
		}
		match, err := vm.expect(`Manual secret: ([A-Z2-7]{32})`, promptTimeout)
		if err != nil {
			return node, err
		}
		node.secret = match[1]
		code, err := credentials.Code(node.secret, time.Now().Unix()/30)
		if err != nil {
			return node, err
		}
		if err := vm.send(code + "\n"); err != nil {
			return node, err
		}
		if err := vm.prompt("Save your recovery codes"); err != nil {
			return node, err
		}
		for range 10 {
			code, err := vm.expect(`\b[0-9a-f]{32}\b`, promptTimeout)
			if err != nil {
				return node, err
			}
			node.recovery = append(node.recovery, code[0])
		}
		if err := vm.reply("Type SAVED", "SAVED\n", promptTimeout); err != nil {
			return node, err
		}
	}
	if err := vm.reply("Review configuration", "\n", promptTimeout); err != nil {
		return node, err
	}
	match, err := vm.expect("Rebooting in|Installation failed", promptTimeout)
	if err != nil {
		return node, err
	}
	if match[0] == "Installation failed" {
		status, err := s.guest("cat /run/floatlab-installer/status.json")
		return node, errors.Join(fmt.Errorf("installer failed: %s", status), err)
	}
	// Suspend the TUI while inspecting the stack before reboot.
	if _, err := s.guest("systemctl kill --signal=STOP --kill-whom=main floatlab-installer-console@ttyS0"); err != nil {
		return node, err
	}
	id, step, err := s.waitStack(vm, "", node.secret)
	if err != nil {
		return node, err
	}
	node.id = id
	if _, err := s.guest(`systemctl is-active --quiet floatlab-installer-root;
		test "$(findmnt -n -o SOURCE /run/floatlab-installer/root)" = floatlab/os/installer;
		chroot /run/floatlab-installer/root /run/current-system/sw/bin/docker info >/dev/null`); err != nil {
		return node, err
	}
	if s.totp {
		code, err := credentials.Code(node.secret, step)
		if err != nil {
			return node, err
		}
		if err := s.rejectLogin(map[string]string{"totp_code": code}); err != nil {
			return node, err
		}
		factor := map[string]string{"recovery_code": node.recovery[0]}
		if _, err := s.login(factor); err != nil {
			return node, err
		}
		if err := s.rejectLogin(factor); err != nil {
			return node, err
		}
	}
	snapshot, err := s.guest(`zfs get -H -o value guid "$(zfs get -H -o value origin floatlab/os/current)"`)
	if err != nil {
		return node, err
	}
	node.snapshot = strings.TrimSpace(snapshot)
	if _, err := s.guest(`test "$(zpool get -H -o value compatibility floatlab)" = openzfs-2.3-linux;
		test "$(zpool get -H -o value bootfs floatlab)" = floatlab/os/current;
		for disk in /dev/disk/by-id/virtio-floatlab-installer-*; do
			case "$disk" in *-part*) continue;; esac
			test "$(lsblk -nr -o TYPE "$disk" | grep -cx part)" = 2
			test "$(blkid -s TYPE -o value "$disk-part2")" = zfs_member
		done;
		echo persistent > /floatlab/system/etc/os-persistence-test;
		systemctl kill --signal=CONT --kill-whom=main floatlab-installer-console@ttyS0`); err != nil {
		return node, err
	}
	if err := vm.send("\n"); err != nil {
		return node, err
	}
	return node, vm.wait(promptTimeout)
}

func fill(value string) string { return "\x01\x0b" + value + "\n" }

func (s scenario) checkInterrupted(vm *terminal, count int, partitions string) error {
	if s.beforeStorage {
		if err := s.waitGuest(vm, "test -e /run/floatlab-installer-test/wipefs-entered"); err != nil {
			return err
		}
		if _, err := s.guest("systemctl kill --signal=KILL --kill-whom=all floatlab-installer-console@ttyS0"); err != nil {
			return err
		}
		if err := vm.prompt("Installation interrupted"); err != nil {
			return err
		}
	} else {
		if err := vm.prompt("Injected bootloader failure"); err != nil {
			return err
		}
		var err error
		partitions, err = s.guest("lsblk -nro PARTUUID | sort")
		if err != nil {
			return err
		}
		time.Sleep(2 * time.Second)
	}
	current, err := s.guest("test ! -e /floatlab/system/etc/installed; lsblk -nro PARTUUID | sort")
	if err != nil {
		return err
	}
	if current != partitions {
		return errors.New("partition UUIDs changed after interruption or failure")
	}
	if s.beforeStorage {
		fmt.Printf("PASS: %d-disk interrupted preparation refuses automatic erase/retry\n", count)
		return nil
	}
	if err := vm.send("\n"); err != nil {
		return err
	}
	if err := vm.wait(promptTimeout); err != nil {
		return err
	}
	fmt.Printf("PASS: %d-disk failure stays visible until manual reboot\n", count)
	return nil
}
