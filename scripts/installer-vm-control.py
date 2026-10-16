#!/usr/bin/env python3
"""Refresh the preview through its guest agent or capture its display through QMP."""
import argparse
import json
import socket
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("action", choices=["refresh", "screenshot"])
parser.add_argument("--output", default="/workspace/.local/installer/vm/screen.ppm")
args = parser.parse_args()

with socket.socket(socket.AF_UNIX) as connection:
    connection.settimeout(5)
    try:
        connection.connect("/tmp/floatlab-preview-guest.sock" if args.action == "refresh" else "/tmp/floatlab-preview-qmp.sock")
    except (FileNotFoundError, ConnectionRefusedError):
        raise SystemExit(1)
    stream = connection.makefile("rwb")
    if args.action == "screenshot":
        json.loads(stream.readline())  # QMP greeting.

    def request(command, arguments=None):
        stream.write((json.dumps({"execute": command, "arguments": arguments or {}}) + "\n").encode())
        stream.flush()
        while True:
            response = json.loads(stream.readline())
            if "error" in response:
                raise RuntimeError(response["error"])
            if "return" in response:
                return response["return"]

    if args.action == "refresh":
        pid = request("guest-exec", {
            "path": "/run/current-system/sw/bin/systemctl",
            "arg": ["restart", "floatlab-installer-display.service"],
        })["pid"]
        for _ in range(50):
            status = request("guest-exec-status", {"pid": pid})
            if status.get("exited"):
                if status.get("exitcode") != 0:
                    raise RuntimeError("Installer display restart failed")
                break
            time.sleep(0.1)
        else:
            raise TimeoutError("Installer display restart timed out")
    else:
        request("qmp_capabilities")
        request("screendump", {"filename": args.output})
