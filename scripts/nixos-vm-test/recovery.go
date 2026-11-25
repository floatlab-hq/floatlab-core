package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/qemu"
)

func testRecovery(ctx context.Context, host *vm) error {
	if err := host.boot(ctx); err != nil {
		return err
	}
	const ready = "systemctl is-active --quiet multi-user.target"
	if err := host.wait(ctx, 3*time.Minute, ready); err != nil {
		return err
	}
	const credentials = "/floatlab/system/etc"
	hash, err := host.run(ctx, `umask 077
mkpasswd -m bcrypt console-secret > `+credentials+`/root-password.hash
ssh-keygen -q -t ed25519 -N "" -f /root/test-key
install -m 600 /root/test-key.pub `+credentials+`/root-authorized-keys
cat `+credentials+`/root-password.hash`)
	if err != nil {
		return err
	}
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return errors.New("recovery password hash is empty")
	}

	const ssh = "ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 "
	for range 2 {
		if err := host.reboot(ctx); err != nil {
			return err
		}
		if err := host.wait(ctx, 3*time.Minute, ready+"; systemctl is-active --quiet sshd.service; timeout 1 bash -c 'true </dev/tcp/127.0.0.1/22'"); err != nil {
			return err
		}
		if _, err := host.run(ctx, ssh+"-o BatchMode=yes -i /root/test-key root@localhost true"); err != nil {
			return err
		}
		for _, check := range []struct {
			command string
			exit    int
		}{
			{ssh + "-o BatchMode=yes -i /root/test-key reader@localhost true", 255},
			// sshpass reports a rejected password as exit 5.
			{"sshpass -p console-secret " + ssh + "-o PubkeyAuthentication=no -o PreferredAuthentications=password root@localhost true", 5},
			{"runuser -u reader -- cat " + credentials + "/root-password.hash", 1},
			{`printf '%s\n' wrong | pamtester login root authenticate`, 1},
		} {
			_, err := host.run(ctx, check.command)
			var exit *qemu.GuestExitError
			if !errors.As(err, &exit) || exit.Code != check.exit || exit.Signal != 0 {
				return fmt.Errorf("expected exit %d from %q, got %v", check.exit, check.command, err)
			}
		}
		info, err := host.run(ctx, "stat -c %u:%g:%a "+credentials+"/root-authorized-keys "+credentials+"/root-password.hash")
		if err != nil {
			return err
		}
		if strings.TrimSpace(info) != "0:0:600\n0:0:600" {
			return fmt.Errorf("wrong recovery file ownership or mode: %q", info)
		}
		shadow, err := host.run(ctx, "getent shadow root")
		if err != nil {
			return err
		}
		fields := strings.Split(strings.TrimSpace(shadow), ":")
		if len(fields) < 2 || fields[1] != hash {
			return errors.New("root shadow does not contain the persisted recovery password hash")
		}
		if _, err := host.run(ctx, `printf '%s\n' console-secret | pamtester login root authenticate acct_mgmt`); err != nil {
			return err
		}
	}
	fmt.Println("PASS: recovery password and root SSH key persisted across both reboots")
	return nil
}
