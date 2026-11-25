//go:build linux || darwin || freebsd

package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (s scenario) boot(node installedNode, selected []int, iteration int) error {
	var members []string
	for _, index := range selected {
		members = append(members, strconv.Itoa(index))
	}
	label := "boot-" + strings.Join(members, "-") + "-" + strconv.Itoa(iteration)
	vm, closeVM, err := s.spawn(selected, false, label)
	if err != nil {
		return err
	}
	defer closeVM()
	if _, _, err := s.waitStack(vm, node.id, node.secret); err != nil {
		return err
	}
	if err := s.waitGuest(vm, `systemctl is-active --quiet floatlab-core-stack;
		curl -fsS http://127.0.0.1:8428/health >/dev/null;
		curl -fsS http://127.0.0.1:9428/health >/dev/null;
		systemctl is-active --quiet systemd-networkd-wait-online`); err != nil {
		return err
	}
	output, err := s.guest("hostname; timedatectl show -p Timezone --value; test ! -e /floatlab/system/etc/bootstrap.json && echo SETTINGS_OK")
	if err != nil {
		return err
	}
	if !slices.Equal(strings.Split(strings.TrimSpace(output), "\n"), []string{s.name, s.timezone, "SETTINGS_OK"}) {
		return fmt.Errorf("installed settings differ: %q", output)
	}
	if _, err := s.guest(`test "$(stat -c %u:%g:%a /floatlab/system/etc/root-password.hash)" = 0:0:600;
		! runuser -u nobody -- cat /floatlab/system/etc/root-password.hash >/dev/null 2>&1;
		systemctl stop serial-getty@ttyS0;
		printf '\nFLOATLAB_RECOVERY_CONSOLE\n' > /dev/ttyS0;
		systemctl start serial-getty@ttyS0`); err != nil {
		return err
	}
	if err := vm.prompt("FLOATLAB_RECOVERY_CONSOLE"); err != nil {
		return err
	}
	for _, step := range []struct{ pattern, send string }{
		{"login:", "root\n"}, {"Password:", "wrong-recovery-password\n"}, {"Login incorrect", ""},
		{"login:", "root\n"}, {"Password:", consolePassword + "\n"}, {"root@" + s.name, "printf '\\nRECOVERY_ROOT=%s\\n' \"$(id -u)\"\n"},
		{"RECOVERY_ROOT=0", "exit\n"}, {"login:", ""},
	} {
		if err := vm.reply(step.pattern, step.send, promptTimeout); err != nil {
			return err
		}
	}
	guid, err := s.guest(`test "$(findmnt -n -o SOURCE /)" = floatlab/os/live;
		! zfs list floatlab/os/installer >/dev/null 2>&1;
		test ! -e /os-ephemeral-test;
		test "$(cat /floatlab/system/etc/os-persistence-test)" = persistent;
		origin=$(zfs get -H -o value origin floatlab/os/current);
		test "$(zfs get -H -o value origin floatlab/os/live)" = "$origin";
		test "$(zfs get -H -o value readonly floatlab/os/current)" = on;
		test "$(zfs get -H -o value readonly "${origin%@base}")" = on;
		zfs get -H -o value guid "$origin"`)
	if err != nil {
		return err
	}
	if strings.TrimSpace(guid) != node.snapshot {
		return fmt.Errorf("installed snapshot changed: %q, want %q", guid, node.snapshot)
	}
	marker := fmt.Sprintf("floatlab-journal-test-%d", time.Now().UnixNano())
	if _, err := s.guest("echo dirty > /os-ephemeral-test; logger -t floatlab-journal-test " + quote(marker)); err != nil {
		return err
	}
	if err := s.waitGuest(vm, "curl -fsSG --data-urlencode "+quote(`query=_msg:"`+marker+`"`)+
		" --data-urlencode limit=1 http://127.0.0.1:9428/select/logsql/query | grep -q "+quote(marker)); err != nil {
		return err
	}
	if _, err := s.guest("test -s /run/floatlab/journal.cursor; systemctl restart floatlab-hostd; test -s /run/floatlab/journal.cursor"); err != nil {
		return err
	}
	if err := s.waitGuest(vm, "systemctl is-active --quiet floatlab-core-stack; curl -fsS http://127.0.0.1:9428/health >/dev/null"); err != nil {
		return err
	}
	if _, err := s.guest(`test "$(systemctl is-active floatlab-installer-console@ttyS0.service)" != active;
		! pgrep -x install-service;
		test -f /floatlab/system/etc/installed`); err != nil {
		return err
	}
	if s.static {
		data, err := s.guest("ip -j -4 addr show dev floatlab-lan")
		if err != nil {
			return err
		}
		var links []struct {
			Addresses []struct{ Local string } `json:"addr_info"`
		}
		if err := json.Unmarshal([]byte(data), &links); err != nil {
			return err
		}
		found := false
		for _, link := range links {
			for _, address := range link.Addresses {
				found = found || address.Local == "10.0.2.20"
			}
		}
		if !found {
			return fmt.Errorf("missing static address: %s", data)
		}
	}
	if s.totp {
		if err := s.rejectLogin(map[string]string{"recovery_code": node.recovery[0]}); err != nil {
			return err
		}
	}
	if len(selected) == len(s.disks) {
		if s.cli != "" && iteration == 0 {
			factor := ""
			if s.totp {
				factor = node.recovery[1]
			}
			if err := s.cliLogin(factor); err != nil {
				return err
			}
			if factor != "" {
				if err := s.rejectLogin(map[string]string{"recovery_code": factor}); err != nil {
					return err
				}
			}
		}
		if s.totp {
			if err := s.repeatBootstrap(vm, node); err != nil {
				return err
			}
		}
	}
	if _, err := s.guest("systemd-run --on-active=2s systemctl reboot"); err != nil {
		return err
	}
	return vm.wait(promptTimeout)
}
