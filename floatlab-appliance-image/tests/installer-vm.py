#!/usr/bin/env python3
"""Exercise the real console installer and boot every installed mirror member.

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
        child = pexpect.spawn(command[0], command[1:], encoding="utf-8", timeout=1200)
        # Read logs only: do not log sent passwords.
        child.logfile_read = open(directory / (label + ".log"), "w")
        return child

    child = spawn(range(count), True, "install")
    name = f"installed-{count}"
    secret = None
    recovery_codes = []
    try:
        child.expect("press Enter to open the console", timeout=600)
        if args.before_storage:
            environment = shlex.split(guest_command(guest_socket, "systemctl show -p Environment --value floatlab-install-worker"))
            worker_path = next(value[5:] for value in environment if value.startswith("PATH="))
            guest_command(guest_socket, "mkdir -p /run/floatlab-installer-test /run/systemd/system/floatlab-install-worker.service.d; "
                          "cat > /run/floatlab-installer-test/wipefs <<'HOOK'\n"
                          "#!/run/current-system/sw/bin/sh\n"
                          "touch /run/floatlab-installer-test/wipefs-entered\nsleep 600\nHOOK\n"
                          "chmod 700 /run/floatlab-installer-test/wipefs; "
                          "printf '%s\\n' '[Service]' " + shlex.quote('Environment="PATH=/run/floatlab-installer-test:' + worker_path + '"') +
                          " > /run/systemd/system/floatlab-install-worker.service.d/interruption.conf; systemctl daemon-reload")
            partition_ids = guest_command(guest_socket, "lsblk -nro PARTUUID | sort")
        child.sendline("")
        child.expect("Disk numbers")
        child.sendline(",".join(str(i + 1) for i in range(count)))
        child.expect("ZFS command")
        child.sendline("")
        child.expect("Type ERASE ALL DATA")
        child.sendline("ERASE ALL DATA")
        child.expect("Timezone")
        if args.before_storage:
            # Stop at the first destructive-command boundary, before it can erase anything.
            wait_guest(guest_socket, "test -e /run/floatlab-installer-test/wipefs-entered", child)
            guest_command(guest_socket, "systemctl kill --signal=KILL --kill-whom=all floatlab-install-worker; "
                          "systemctl kill -s HUP floatlab-installer-console@ttyS0.service")
            child.expect("press Enter to open the console")
            child.sendline("")
            child.expect("interrupted disk preparation")
            assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
            guest_command(guest_socket, 'test "$(systemctl is-active floatlab-install-worker)" != active; '
                          "test ! -e /floatlab/system/etc/installed")
            child.close(force=True)
            child.logfile_read.close()
            child = spawn(range(count), True, "before-storage-recovery")
            child.expect("press Enter to open the console", timeout=600)
            assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
            guest_command(guest_socket, 'test "$(systemctl is-active floatlab-install-worker)" != active')
            print(f"PASS: {count}-disk interruption before storage checkpoint refuses automatic erase/reboot", flush=True)
            shutil.rmtree(socket_directory)
            if not args.keep:
                shutil.rmtree(directory)
            return
        child.sendline(args.timezone)
        child.expect("Node name")
        child.sendline(name)
        child.expect("IPv4 mode")
        child.sendline("static" if args.static else "dhcp")
        if args.static:
            for prompt, value in [("IPv4 address", "10.0.2.20"), ("Netmask", "255.255.255.0"),
                                  ("Gateway", "10.0.2.2"), ("DNS servers", "10.0.2.3")]:
                child.expect(prompt)
                child.sendline(value)
        child.expect("Administrator username")
        if args.totp:
            wait_guest(guest_socket, 'test "$(timedatectl show -p NTPSynchronized --value)" = yes', child)
        if args.failures:
            # Leave the real worker alive when the console disconnects.
            guest_command(guest_socket, "systemctl kill -s HUP floatlab-installer-console@ttyS0.service")
            child.expect("press Enter to open the console")
            child.sendline("")
            child.expect("Recovering an incomplete installation")
            child.expect("Timezone")
            child.sendline("")
            child.expect("Node name")
            child.sendline("")
            child.expect("IPv4 mode")
            child.sendline("")
            if args.static:
                for prompt in ["IPv4 address", "Netmask", "Gateway", "DNS servers"]:
                    child.expect(prompt)
                    child.sendline("")
            child.expect("Administrator username")
            wait_guest(guest_socket, "jq -e '.storage_ready == true' /run/floatlab-installer/status.json", child)
            partition_ids = guest_command(guest_socket, "lsblk -nro PARTUUID | sort")
            identity = guest_command(guest_socket, "jq -c .identity /run/floatlab-installer/operation.json")
            # Reboot after the durable checkpoint; runtime state disappears.
            guest_command(guest_socket, "systemctl stop floatlab-install-worker; zpool export floatlab; "
                          "systemd-run --on-active=2s systemctl reboot")
            child.expect(pexpect.EOF)
            child.close(force=True)
            child.logfile_read.close()
            child = spawn(range(count), True, "checkpoint-recovery")
            child.expect("press Enter to open the console", timeout=600)
            guest_command(guest_socket, 'systemctl mask --runtime --now systemd-timesyncd; date -s "$(date -Iseconds)"')
            child.sendline("")
            child.expect("Recovering an incomplete installation")
            child.expect("Timezone")
            child.sendline(args.timezone)
            child.expect("Node name")
            child.sendline(name)
            child.expect("IPv4 mode")
            child.sendline("static" if args.static else "dhcp")
            if args.static:
                for prompt, value in [("IPv4 address", "10.0.2.20"), ("Netmask", "255.255.255.0"),
                                      ("Gateway", "10.0.2.2"), ("DNS servers", "10.0.2.3")]:
                    child.expect(prompt)
                    child.sendline(value)
            child.expect("Administrator username")
            assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
            assert guest_command(guest_socket, "jq -c .identity /run/floatlab-installer/operation.json") == identity
            # Force a genuine registry download failure, without external outage dependencies.
            guest_command(guest_socket, "systemctl stop floatlab-install-worker; "
                          "systemctl reset-failed floatlab-install-worker; "
                          "umask 077; jq '.error = \"\"' /run/floatlab-installer/status.json > /run/floatlab-installer/status.tmp; "
                          "mv /run/floatlab-installer/status.tmp /run/floatlab-installer/status.json; "
                          "install -d -m 700 /run/floatlab-installer-test; "
                          "systemctl start floatlab-core-stack-seed; "
                          "cp /floatlab/system/docker-compose.yml /run/floatlab-installer-test/compose.orig; "
                          "sed -i 's@rqlite/rqlite:8.26.0@127.0.0.1:1/unavailable:installer-test@' /floatlab/system/docker-compose.yml")
        child.sendline("installer-test")
        child.expect("Password:")
        child.sendline("installer-test-password")
        child.expect("Confirm password:")
        child.sendline("installer-test-password")
        result = child.expect(["Set up TOTP", "TOTP enrollment is unavailable"])
        if args.failures:
            assert result == 1, "NTP-disabled guest unexpectedly allowed enrollment"
        if args.totp:
            assert result == 0, "TOTP test requires real NTP synchronization"
            child.sendline("yes")
            child.expect(r"Manual secret: ([A-Z2-7]{32})")
            secret = child.match.group(1)
            child.expect("Six-digit authenticator code")
            child.sendline(totp(secret, int(time.time()) // 30))
            child.expect("Save these single-use recovery codes:")
            for _ in range(10):
                child.expect(r"\b[0-9a-f]{32}\b")
                recovery_codes.append(child.match.group(0))
            child.expect("Type SAVED")
            child.sendline("SAVED")
        elif result == 0:
            child.sendline("no")
        if args.failures:
            child.expect("Step 4")
            guest_command(guest_socket, "jq .admin /run/floatlab-installer/submission.json > /run/floatlab-installer-test/bootstrap.orig; "
                          "chmod 600 /run/floatlab-installer-test/bootstrap.orig; systemctl start --no-block floatlab-install-worker")
            child.expect("Installation failed:")
            child.expect("Retry finalization")
            assert "context canceled" not in child.before, child.before
            assert guest_command(guest_socket, "test ! -e /floatlab/system/etc/installed; lsblk -nro PARTUUID | sort") == partition_ids
            guest_command(guest_socket, "cp /run/floatlab-installer-test/compose.orig /floatlab/system/docker-compose.yml; "
                          "systemctl unmask --runtime systemd-timesyncd; systemctl start systemd-timesyncd")
            child.sendline("yes")
        if child.expect(["Rebooting in 30 seconds", "Installation failed:"]) == 1:
            child.expect("Retry finalization")
            raise AssertionError("installer failed: " + child.before.strip())
        child.sendline("")
        child.expect("Automatic reboot cancelled")
        child.expect("Type REBOOT")
        node_id, verified_step = wait_stack(base, name, secret=secret)
        if secret:
            factor_code = totp(secret, verified_step)
            rejected_login(base, {"totp_code": factor_code})
            login(base, {"recovery_code": recovery_codes[0]})
            rejected_login(base, {"recovery_code": recovery_codes[0]})
        if args.failures:
            guest_command(guest_socket, "cp /run/floatlab-installer-test/bootstrap.orig /floatlab/system/etc/bootstrap.json; "
                          "cd /floatlab/system; docker compose restart floatlab-control")
            assert wait_stack(base, name, node_id)[0] == node_id
            wait_guest(guest_socket, "test ! -e /floatlab/system/etc/bootstrap.json", child)
            assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
        child.sendline("REBOOT")
        child.expect(pexpect.EOF)
    except Exception:
        try:
            print(guest_command(guest_socket, "systemctl status floatlab-installer-console@ttyS0.service --no-pager; journalctl -u floatlab-install-worker -n 20 --no-pager"), flush=True)
        except Exception as diagnostic:
            print("Guest diagnostics unavailable:", diagnostic, flush=True)
        raise
    finally:
        child.close(force=True)
        child.logfile_read.close()

    # Reboot without installation media; test each mirror member independently.
    boot_cases = [(selected, False) for selected in ([list(range(count))] + [[i] for i in range(count)] if count > 1 else [[0]])]
    if args.failures:
        boot_cases.append((list(range(count)), True))
    for selected, legacy in boot_cases:
        label = "boot-existing" if legacy else "boot-" + "-".join(str(i) for i in selected)
        child = spawn(selected, False, label)
        try:
            wait_stack(base, name, node_id, child, secret)
            wait_guest(guest_socket, "systemctl is-active --quiet floatlab-core-stack; "
                       "curl -fsS http://127.0.0.1:8428/health >/dev/null; "
                       "curl -fsS http://127.0.0.1:9428/health >/dev/null; "
                       "systemctl is-active --quiet systemd-networkd-wait-online", child)
            output = guest_command(guest_socket, "hostname; timedatectl show -p Timezone --value; "
                                   "test ! -e /floatlab/system/etc/bootstrap.json && echo SETTINGS_OK")
            assert output.splitlines() == [name, args.timezone, "SETTINGS_OK"], output
            guest_command(guest_socket, 'test "$(systemctl is-active floatlab-installer-console@ttyS0.service)" != active; '
                          'test "$(systemctl is-active floatlab-install-worker.service)" != active')
            if not legacy:
                guest_command(guest_socket, "test -f /floatlab/system/etc/installed")
            if args.failures:
                assert guest_command(guest_socket, "lsblk -nro PARTUUID | sort") == partition_ids
            if args.static:
                output = guest_command(guest_socket, "ip -j -4 addr show dev floatlab-lan")
                assert any(address["local"] == "10.0.2.20" for link in json.loads(output)
                           for address in link["addr_info"]), output
            if secret:
                rejected_login(base, {"recovery_code": recovery_codes[0]})
            if args.cli and selected == list(range(count)) and not legacy:
                factor = {"recovery_code": recovery_codes[1]} if secret else None
                cli_login(args.cli, directory / "cli-config", base, factor)
                if factor:
                    rejected_login(base, factor)
            if secret and selected == list(range(count)):
                repeat_bootstrap(guest_socket, base, name, node_id, secret, recovery_codes[0], child)
            if args.failures and selected == list(range(count)):
                for marker in ("installed", "install-operation.json"):
                    source, target = (marker + ".saved", marker) if legacy else (marker, marker + ".saved")
                    guest_command(guest_socket, "mv /floatlab/system/etc/" + source + " /floatlab/system/etc/" + target)
            guest_command(guest_socket, "systemctl stop floatlab-core-stack docker docker.socket floatlab-hostd; "
                          "zpool export floatlab; systemd-run --on-active=2s systemctl reboot")
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
    parser.add_argument("--failures", action="store_true", help="exercise console/worker interruption, unavailable NTP, image-pull failure, and repeated bootstrap")
    parser.add_argument("--cli", help="path to compiled FloatLab CLI for installed-appliance interactive login")
    parser.add_argument("--before-storage", action="store_true", help="interrupt at the first wipe command and verify destructive work is not replayed")
    args = parser.parse_args()
    if not args.firmware:
        parser.error("--firmware or FLOATLAB_OVMF is required")
    if args.failures and args.totp:
        parser.error("run unavailable-NTP and TOTP enrollment cases separately")
    for count in ([args.disks] if args.disks else [1, 2]):
        run_case(args, count)
