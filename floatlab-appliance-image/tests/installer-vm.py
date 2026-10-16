#!/usr/bin/env python3
"""Exercise the real TUI installer and boot every installed mirror member.

Runs outside the Nix build sandbox: full-stack verification requires image pulls.
Only creates and modifies disposable qcow2 files inside its temporary directory.
"""
import argparse
import base64
import glob
import hashlib
import hmac
import json
import os
from pathlib import Path
import shutil
import shlex
import socket
import struct
import subprocess
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

import pexpect


def port():
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return sock.getsockname()[1]


def api(base, path, body=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(base + "/api/v1" + path,
                                     data=json.dumps(body).encode() if body else None,
                                     headers=headers)
    with urllib.request.urlopen(request, timeout=10) as response:
        return json.load(response)


def totp(secret, step):
    digest = hmac.new(base64.b32decode(secret + "=" * (-len(secret) % 8)),
                      struct.pack(">Q", step), hashlib.sha1).digest()
    offset = digest[-1] & 15
    return f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}"


def login(base, factor=None):
    return api(base, "/auth/token", {"username": "installer-test", "password": "installer-test-password", **(factor or {})})["access_token"]


def rejected_login(base, factor):
    try:
        login(base, factor)
    except urllib.error.HTTPError as error:
        assert error.code == 401, error
        return
    raise AssertionError("replayed second factor accepted")


def wait_stack(base, name, expected_id=None, child=None, secret=None):
    deadline = time.monotonic() + 900
    last_step = None
    while time.monotonic() < deadline:
        if child is not None:
            try:
                child.read_nonblocking(65536, timeout=0)
            except pexpect.TIMEOUT:
                pass
            except pexpect.EOF:
                raise AssertionError("VM exited before the installed stack became ready")
        try:
            api(base, "/health")
            step = int(time.time()) // 30
            if secret and step == last_step:
                time.sleep(1)
                continue
            last_step = step
            token = login(base, {"totp_code": totp(secret, step)} if secret else None)
            nodes = api(base, "/nodes", token=token)
            # The management API returns the bare node array.
            node = next(n for n in nodes if n["name"] == name)
            assert node["hostname"] == name, node
            if expected_id:
                assert node["id"] == expected_id, node
            assert api(base, "/nodes/" + node["id"] + "/health", token=token)["status"] == "reachable"
            return node["id"], step
        except (OSError, urllib.error.URLError, KeyError, StopIteration):
            time.sleep(1)
    raise AssertionError("installed stack did not become healthy at " + base)


def guest_command(path, command):
    command = 'set -e; export PATH="/run/current-system/sw/bin:$PATH"; ' + command
    with socket.socket(socket.AF_UNIX) as connection:
        connection.settimeout(30)
        connection.connect(str(path))
        stream = connection.makefile("rwb")
        def rpc(name, arguments):
            stream.write((json.dumps({"execute": name, "arguments": arguments}) + "\n").encode())
            stream.flush()
            response = json.loads(stream.readline())
            if "error" in response:
                raise RuntimeError(response["error"])
            return response["return"]
        pid = rpc("guest-exec", {"path": "/run/current-system/sw/bin/sh", "arg": ["-c", command], "capture-output": True})["pid"]
        for _ in range(60):
            status = rpc("guest-exec-status", {"pid": pid})
            if status.get("exited"):
                output = base64.b64decode(status.get("out-data", "")).decode()
                error = base64.b64decode(status.get("err-data", "")).decode()
                if status.get("exitcode") != 0:
                    raise RuntimeError(error or output)
                return output
            time.sleep(1)
        raise TimeoutError("guest command did not finish")


def wait_guest(path, command, child):
    deadline = time.monotonic() + 600
    while time.monotonic() < deadline:
        try:
            return guest_command(path, command)
        except (OSError, RuntimeError, TimeoutError):
            try:
                child.read_nonblocking(65536, timeout=1)
            except pexpect.TIMEOUT:
                pass
    raise AssertionError("guest condition timed out: " + command)


def cli_login(executable, directory, base, factor=None):
    environment = {key: value for key, value in os.environ.items() if key not in ("FLOATLAB_URL", "FLOATLAB_TOKEN")}
    environment["XDG_CONFIG_HOME"] = str(directory)
    child = pexpect.spawn(str(Path(executable).resolve()), env=environment, encoding="utf-8", timeout=60)
    try:
        for prompt, value in [("FloatLab URL", base), ("Username", "installer-test"), ("Password", "installer-test-password")]:
            child.expect(prompt)
            child.sendline(value)
        if factor:
            child.expect("Second factor")
            if "recovery_code" in factor:
                child.send("\x1b[B")
            child.sendline("")
            child.expect("Single-use recovery code" if "recovery_code" in factor else "Six-digit authenticator code")
            child.sendline(next(iter(factor.values())))
        child.expect(r"\? floatlab")
        child.sendline("exit")
        child.expect(pexpect.EOF)
        child.close()
        assert child.exitstatus == 0, child.exitstatus
        session = json.loads((directory / "floatlab/config.json").read_text())
        assert api(base, "/nodes", token=session["token"])
    finally:
        child.close(force=True)


def repeat_bootstrap(path, base, name, node_id, secret, used_code, child):
    query_url = "http://127.0.0.1:4001/db/query?" + urllib.parse.urlencode({
        "q": "SELECT password_hash,totp_secret FROM users JOIN user_credentials USING(username) WHERE username='installer-test'"})
    command = "cd /floatlab/system; docker compose exec -T rqlite wget -qO- " + shlex.quote(query_url)
    original = json.loads(guest_command(path, command))["results"][0]["values"][0]
    password_hash = original[0]
    bootstrap = {"username": "installer-test", "node_id": node_id, "node_name": name,
                 "password_hash": password_hash[:29] + ("A" if password_hash[29] != "A" else "B") + password_hash[30:],
                 "totp_secret": "", "recovery_hashes": [hashlib.sha256(used_code.encode()).hexdigest()]}
    guest_command(path, "umask 077; printf '%s\\n' " + shlex.quote(json.dumps(bootstrap)) +
                  " > /floatlab/system/etc/bootstrap.json; cd /floatlab/system; docker compose restart floatlab-control")
    wait_guest(path, "test ! -e /floatlab/system/etc/bootstrap.json", child)
    assert json.loads(guest_command(path, command))["results"][0]["values"][0] == original
    wait_stack(base, name, node_id, child, secret)
    rejected_login(base, {"recovery_code": used_code})


def run_case(args, count):
    directory = Path(tempfile.mkdtemp(prefix="floatlab-installer-vm-"))
    print("Installer VM logs:", directory, flush=True)
    disks = []
    for index in range(count):
        disk = directory / f"disk-{index}.qcow2"
        subprocess.run(["qemu-img", "create", "-f", "qcow2", str(disk), "32G"], check=True)
        disks.append(disk)
    firmware = Path(args.firmware)
    code = next(iter(glob.glob(str(firmware / "OVMF_CODE.fd"))), None)
    variables = next(iter(glob.glob(str(firmware / "OVMF_VARS.fd"))), None)
    if not code or not variables:
        raise RuntimeError("OVMF_CODE.fd and OVMF_VARS.fd are required")
    nvram = directory / "vars.fd"
    shutil.copyfile(variables, nvram)
    host_port = port()
    base = f"http://127.0.0.1:{host_port}"

    socket_directory = Path(tempfile.mkdtemp(prefix="floatlab-qga-", dir="/tmp"))
    guest_socket = socket_directory / "guest.sock"

    def spawn(selected, iso, label):
        command = ["qemu-system-x86_64", "-machine", "q35", "-m", "6144", "-smp", "4",
                   "-accel", "kvm" if os.access("/dev/kvm", os.R_OK | os.W_OK) else "tcg",
                   "-drive", "if=pflash,format=raw,readonly=on,file=" + code,
                   "-drive", "if=pflash,format=raw,file=" + str(nvram),
                   "-display", "none", "-serial", "stdio", "-no-reboot",
                   "-netdev", f"user,id=lan,hostfwd=tcp:127.0.0.1:{host_port}-{ '10.0.2.20' if args.static else ''}:8080",
                   "-device", "virtio-net-pci,netdev=lan",
                   "-device", "virtio-serial",
                   "-chardev", f"socket,path={guest_socket},server=on,wait=off,id=agent",
                   "-device", "virtserialport,chardev=agent,name=org.qemu.guest_agent.0"]
        for index in selected:
            command += ["-drive", f"if=none,id=disk{index},format=qcow2,file={disks[index]}",
                        "-device", f"virtio-blk-pci,drive=disk{index},serial=floatlab-installer-{index}"]
        if iso:
            command += ["-cdrom", str(Path(args.iso).resolve()), "-boot", "order=d"]
        else:
            command += ["-boot", "order=c"]
        child = pexpect.spawn(command[0], command[1:], encoding="utf-8", timeout=1200, dimensions=(40, 140))
        # Read logs only: do not log sent passwords.
        child.logfile_read = open(directory / (label + ".log"), "w")
        return child

    child = spawn(range(count), True, "install")
    name = f"installed-{count}"
    secret = None
    recovery_codes = []
    early_return = False
    try:
        child.expect("Select installation disks", timeout=600)
        guest_command(guest_socket, "set -e; test ! -x /run/current-system/sw/bin/docker; "
                      "test ! -x /run/current-system/sw/bin/docker-compose; "
                      "test ! -x /run/current-system/sw/bin/git; "
                      "test -x /run/current-system/sw/bin/nix-shell; "
                      "test ! -e /run/floatlab-installer/root/run/docker.sock")
        if args.before_storage or args.failures:
            environment = shlex.split(guest_command(guest_socket, "systemctl show -p Environment --value floatlab-installer-console@ttyS0"))
            worker_path = next(value[5:] for value in environment if value.startswith("PATH="))
            tool = "wipefs" if args.before_storage else "zbm-kcl"
            script = ("#!/run/current-system/sw/bin/sh\ntouch /run/floatlab-installer-test/wipefs-entered\nsleep 600\n"
                      if args.before_storage else "#!/run/current-system/sw/bin/sh\necho Injected bootloader failure >&2\nexit 1\n")
            guest_command(guest_socket, "mkdir -p /run/floatlab-installer-test /run/systemd/system/floatlab-installer-console@ttyS0.service.d; "
                          "printf '%s' " + shlex.quote(script) + " > /run/floatlab-installer-test/" + tool + "; "
                          "chmod 700 /run/floatlab-installer-test/" + tool + "; "
                          "printf '%s\\n' '[Service]' " + shlex.quote('Environment="PATH=/run/floatlab-installer-test:' + worker_path + '"') +
                          " > /run/systemd/system/floatlab-installer-console@ttyS0.service.d/interruption.conf; "
                          "systemctl daemon-reload; systemctl restart floatlab-installer-console@ttyS0")
            child.expect("Select installation disks")
            partition_ids = guest_command(guest_socket, "lsblk -nro PARTUUID | sort")
        for index in range(count):
            child.send(" ")
            if index + 1 < count:
                child.send("\x1b[B")
        child.sendline("")
        child.expect("Erase All and continue")
        child.sendline("")
        child.expect("Configure FloatLab while installation runs")
        if args.before_storage:
            wait_guest(guest_socket, "test -e /run/floatlab-installer-test/wipefs-entered", child)
            guest_command(guest_socket, "systemctl kill --signal=KILL --kill-whom=all floatlab-installer-console@ttyS0")
            child.expect("Installation interrupted")
            assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
            guest_command(guest_socket, "test ! -e /floatlab/system/etc/installed")
            print(f"PASS: {count}-disk interrupted preparation refuses automatic erase/retry", flush=True)
            early_return = True
            return
        if args.failures:
            child.expect("Injected bootloader failure")
            partition_ids = guest_command(guest_socket, "lsblk -nro PARTUUID | sort")
            time.sleep(2)
            assert guest_command(guest_socket, "test ! -e /floatlab/system/etc/installed; lsblk -nro PARTUUID | sort") == partition_ids
            child.sendline("")
            child.expect(pexpect.EOF)
            print(f"PASS: {count}-disk failure stays visible until manual reboot", flush=True)
            early_return = True
            return
        def fill(value):
            child.send("\x01\x0b")
            child.sendline(value)
        fill(name)
        child.sendline("")  # Open timezone selection.
        child.expect("Choose timezone")
        child.send(args.timezone)
        child.sendline("")
        child.send("\x1b[C" if args.static else "\x1b[D")  # IPv4 radio choice.
        child.sendline("")
        if args.static:
            for value in ["10.0.2.20", "255.255.255.0", "10.0.2.2"]:
                fill(value)
        fill("10.0.2.3")
        child.expect("Set up administrator")  # Network settings passed service validation.
        if args.totp:
            wait_guest(guest_socket, 'test "$(timedatectl show -p NTPSynchronized --value)" = yes', child)
        for value in ["installer-test", "installer-test-password", "installer-test-password"]:
            fill(value)
        child.send("\x1b[D" if args.totp else "\x1b[C")
        child.sendline("")
        if args.totp:
            child.expect("Enroll your authenticator")
            child.expect(r"Manual secret: ([A-Z2-7]{32})")
            secret = child.match.group(1)
            child.sendline(totp(secret, int(time.time()) // 30))
            child.expect("Save your recovery codes")
            for _ in range(10):
                child.expect(r"\b[0-9a-f]{32}\b")
                recovery_codes.append(child.match.group(0))
            child.expect("Type SAVED")
            child.sendline("SAVED")
        child.expect("Review configuration")
        child.sendline("")
        if child.expect(["Rebooting in", "Installation failed"]) == 1:
            raise AssertionError("installer failed: " + guest_command(guest_socket, "cat /run/floatlab-installer/status.json"))
        # Suspend only the TUI while inspecting the installed stack before reboot.
        guest_command(guest_socket, "systemctl kill --signal=STOP --kill-whom=main floatlab-installer-console@ttyS0")
        node_id, verified_step = wait_stack(base, name, secret=secret)
        guest_command(guest_socket, 'set -e; systemctl is-active --quiet floatlab-installer-root; '
                      'test "$(findmnt -n -o SOURCE /run/floatlab-installer/root)" = floatlab/os/installer; '
                      'chroot /run/floatlab-installer/root /run/current-system/sw/bin/docker info >/dev/null')
        if secret:
            rejected_login(base, {"totp_code": totp(secret, verified_step)})
            login(base, {"recovery_code": recovery_codes[0]})
            rejected_login(base, {"recovery_code": recovery_codes[0]})
        snapshot_guid = guest_command(guest_socket, 'zfs get -H -o value guid "$(zfs get -H -o value origin floatlab/os/current)"')
        guest_command(guest_socket, 'set -e; test "$(zpool get -H -o value compatibility floatlab)" = openzfs-2.3-linux; '
                      'test "$(zpool get -H -o value bootfs floatlab)" = floatlab/os/current; '
                      'for disk in /dev/disk/by-id/virtio-floatlab-installer-*; do '
                      'case "$disk" in *-part*) continue;; esac; '
                      'test "$(lsblk -nr -o TYPE "$disk" | grep -cx part)" = 2; '
                      'test "$(blkid -s TYPE -o value "$disk-part2")" = zfs_member; done; '
                      'echo persistent > /floatlab/system/etc/os-persistence-test')
        guest_command(guest_socket, "systemctl kill --signal=CONT --kill-whom=main floatlab-installer-console@ttyS0")
        child.sendline("")
        child.expect(pexpect.EOF)
    except Exception:
        try:
            print(guest_command(guest_socket, "journalctl -u floatlab-installer-console@ttyS0 "
                                "-u floatlab-installer-root -u docker -u floatlab-hostd -u floatlab-core-stack "
                                "-n 80 --no-pager; tail -n 20 /tmp/install-tui-*.log"), flush=True)
        except Exception as diagnostic:
            print("Guest diagnostics unavailable:", diagnostic, flush=True)
        raise
    finally:
        child.close(force=True)
        child.logfile_read.close()
        if early_return:
            shutil.rmtree(socket_directory)
            if not args.keep:
                shutil.rmtree(directory)

    # Reboot without installation media; test each mirror member independently.
    boot_cases = [(selected, False, iteration) for selected in ([list(range(count))] + [[i] for i in range(count)] if count > 1 else [[0]]) for iteration in range(2)]
    for selected, legacy, iteration in boot_cases:
        label = "boot-existing" if legacy else "boot-" + "-".join(str(i) for i in selected)
        child = spawn(selected, False, label + "-" + str(iteration))
        try:
            wait_stack(base, name, node_id, child, secret)
            wait_guest(guest_socket, "systemctl is-active --quiet floatlab-core-stack; "
                       "curl -fsS http://127.0.0.1:8428/health >/dev/null; "
                       "curl -fsS http://127.0.0.1:9428/health >/dev/null; "
                       "systemctl is-active --quiet systemd-networkd-wait-online", child)
            output = guest_command(guest_socket, "hostname; timedatectl show -p Timezone --value; "
                                   "test ! -e /floatlab/system/etc/bootstrap.json && echo SETTINGS_OK")
            assert output.splitlines() == [name, args.timezone, "SETTINGS_OK"], output
            output = guest_command(guest_socket, 'set -e; test "$(findmnt -n -o SOURCE /)" = floatlab/os/live; '
                                   '! zfs list floatlab/os/installer >/dev/null 2>&1; '
                                   'test ! -e /os-ephemeral-test; '
                                   'test "$(cat /floatlab/system/etc/os-persistence-test)" = persistent; '
                                   'origin=$(zfs get -H -o value origin floatlab/os/current); '
                                   'test "$(zfs get -H -o value origin floatlab/os/live)" = "$origin"; '
                                   'test "$(zfs get -H -o value readonly floatlab/os/current)" = on; '
                                   'test "$(zfs get -H -o value readonly "${origin%@base}")" = on; '
                                   'zfs get -H -o value guid "$origin"')
            assert output.strip() == snapshot_guid.strip(), output
            guest_command(guest_socket, 'echo dirty > /os-ephemeral-test')
            marker = "floatlab-journal-test-" + str(time.time_ns())
            guest_command(guest_socket, "logger -t floatlab-journal-test " + shlex.quote(marker))
            wait_guest(guest_socket, "curl -fsSG --data-urlencode " + shlex.quote('query=_msg:"' + marker + '"') +
                       " --data-urlencode limit=1 http://127.0.0.1:9428/select/logsql/query | grep -q " + shlex.quote(marker), child)
            guest_command(guest_socket, "test -s /run/floatlab/journal.cursor; systemctl restart floatlab-hostd; "
                          "test -s /run/floatlab/journal.cursor")
            # Restarting hostd also restarts its dependent core stack.
            wait_guest(guest_socket, "systemctl is-active --quiet floatlab-core-stack; "
                       "curl -fsS http://127.0.0.1:9428/health >/dev/null", child)
            guest_command(guest_socket, 'test "$(systemctl is-active floatlab-installer-console@ttyS0.service)" != active; '
                          '! pgrep -x install-service')
            if not legacy:
                guest_command(guest_socket, "test -f /floatlab/system/etc/installed")
            if args.static:
                output = guest_command(guest_socket, "ip -j -4 addr show dev floatlab-lan")
                assert any(address["local"] == "10.0.2.20" for link in json.loads(output)
                           for address in link["addr_info"]), output
            if secret:
                rejected_login(base, {"recovery_code": recovery_codes[0]})
            if args.cli and selected == list(range(count)) and not legacy and iteration == 0:
                factor = {"recovery_code": recovery_codes[1]} if secret else None
                cli_login(args.cli, directory / "cli-config", base, factor)
                if factor:
                    rejected_login(base, factor)
            if secret and selected == list(range(count)):
                repeat_bootstrap(guest_socket, base, name, node_id, secret, recovery_codes[0], child)
            guest_command(guest_socket, "systemd-run --on-active=2s systemctl reboot")
            child.expect(pexpect.EOF)
        finally:
            child.close(force=True)
            child.logfile_read.close()
    shutil.rmtree(socket_directory)
    print(f"PASS: {count}-disk console installation and disk-only boots", flush=True)
    if not args.keep:
        shutil.rmtree(directory)


if __name__ == "__main__":
    assert totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", 1) == "287082"  # RFC 6238 fixture.
    parser = argparse.ArgumentParser()
    parser.add_argument("--iso", required=True)
    parser.add_argument("--firmware", default=os.environ.get("FLOATLAB_OVMF"))
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--disks", type=int, choices=[1, 2], help="default: test both layouts")
    parser.add_argument("--timezone", default="UTC")
    parser.add_argument("--static", action="store_true", help="install and verify static IPv4 on the QEMU user network")
    parser.add_argument("--totp", action="store_true", help="enroll TOTP and verify replay and recovery-code persistence")
    parser.add_argument("--failures", action="store_true", help="inject a bootloader failure and verify it remains terminal until manual reboot")
    parser.add_argument("--cli", help="path to compiled FloatLab CLI for installed-appliance interactive login")
    parser.add_argument("--before-storage", action="store_true", help="interrupt at the first wipe command and verify destructive work is not replayed")
    args = parser.parse_args()
    if not args.firmware:
        parser.error("--firmware or FLOATLAB_OVMF is required")
    if args.failures and args.before_storage:
        parser.error("run failure and interruption cases separately")
    for count in ([args.disks] if args.disks else [1, 2]):
        run_case(args, count)
