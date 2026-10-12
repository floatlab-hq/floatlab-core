#!/usr/bin/env python3
"""Exercise the real console installer and boot every installed mirror member.

Runs outside the Nix build sandbox: full-stack verification requires image pulls.
Only creates and modifies disposable qcow2 files inside its temporary directory.
"""
import argparse
import base64
import glob
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import urllib.error
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


def wait_stack(base, name, expected_id=None, child=None):
    deadline = time.monotonic() + 900
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
            token = api(base, "/auth/token", {"username": "installer-test", "password": "installer-test-password"})["access_token"]
            nodes = api(base, "/nodes", token=token)
            # The management API returns the bare node array.
            node = next(n for n in nodes if n["name"] == name)
            assert node["hostname"] == name, node
            if expected_id:
                assert node["id"] == expected_id, node
            assert api(base, "/nodes/" + node["id"] + "/health", token=token)["status"] == "reachable"
            return node["id"]
        except (OSError, urllib.error.URLError, KeyError, StopIteration):
            time.sleep(1)
    raise AssertionError("installed stack did not become healthy at " + base)


def guest_command(path, command):
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
                   "-netdev", f"user,id=lan,hostfwd=tcp:127.0.0.1:{host_port}-:8080",
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
    try:
        child.expect("press Enter to open the console", timeout=600)
        child.sendline("")
        child.expect("Disk numbers")
        child.sendline(",".join(str(i + 1) for i in range(count)))
        child.expect("ZFS command")
        child.sendline("")
        child.expect("Type ERASE ALL DATA")
        child.sendline("ERASE ALL DATA")
        child.expect("Timezone")
        child.sendline("UTC")
        child.expect("Node name")
        child.sendline(name)
        child.expect("IPv4 mode")
        child.sendline("dhcp")
        child.expect("Administrator username")
        child.sendline("installer-test")
        child.expect("Password:")
        child.sendline("installer-test-password")
        child.expect("Confirm password:")
        child.sendline("installer-test-password")
        result = child.expect(["Set up TOTP", "TOTP enrollment is unavailable"])
        if result == 0:
            child.sendline("no")
        if child.expect(["Rebooting in 30 seconds", "Installation failed:"]) == 1:
            child.expect("Retry finalization")
            raise AssertionError("installer failed: " + child.before.strip())
        child.sendline("")
        child.expect("Automatic reboot cancelled")
        child.expect("Type REBOOT")
        node_id = wait_stack(base, name)
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
    for selected in ([list(range(count))] + [[i] for i in range(count)] if count > 1 else [[0]]):
        label = "boot-" + "-".join(str(i) for i in selected)
        child = spawn(selected, False, label)
        try:
            wait_stack(base, name, node_id, child)
            output = guest_command(guest_socket, "hostname; timedatectl show -p Timezone --value; "
                                   "test ! -e /floatlab/system/etc/bootstrap.json && echo SETTINGS_OK")
            assert output.splitlines() == [name, "UTC", "SETTINGS_OK"], output
            guest_command(guest_socket, "systemctl stop floatlab-core-stack docker docker.socket floatlab-hostd && zpool export floatlab && systemctl reboot --no-block")
            child.expect(pexpect.EOF)
        finally:
            child.close(force=True)
            child.logfile_read.close()
    shutil.rmtree(socket_directory)
    print(f"PASS: {count}-disk console installation and disk-only boots", flush=True)
    if not args.keep:
        shutil.rmtree(directory)


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--iso", required=True)
    parser.add_argument("--firmware", default=os.environ.get("FLOATLAB_OVMF"))
    parser.add_argument("--keep", action="store_true")
    parser.add_argument("--disks", type=int, choices=[1, 2], help="default: test both layouts")
    args = parser.parse_args()
    if not args.firmware:
        parser.error("--firmware or FLOATLAB_OVMF is required")
    for count in ([args.disks] if args.disks else [1, 2]):
        run_case(args, count)
