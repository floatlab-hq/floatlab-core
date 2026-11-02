"""Check answers discovery on boot media without mounting or changing real disks."""
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile

script = Path(sys.argv[1]).resolve()
with tempfile.TemporaryDirectory() as temporary:
    root = Path(temporary)
    media, runtime, commands = [root / name for name in ("iso", "runtime", "bin")]
    for directory in (media, runtime, commands):
        directory.mkdir()
    environment = {**os.environ, "PATH": str(commands) + ":" + os.environ["PATH"],
                   "TEST_ROOT": str(root), "TEST_PARENT": ""}
    mocks = {
        "findmnt": 'case "$*" in *SOURCE*) echo /dev/testusb1;; *) exit 1;; esac',
        "lsblk": 'case "$*" in *PKNAME*) echo "$TEST_PARENT";; *TYPE*) if [ -n "$TEST_PARENT" ]; then printf "/dev/testusb1 part\\n/dev/testusb2 part\\n"; else echo rom; fi;; esac',
        "mount": 'test "$2" = ro,nodev,nosuid,noexec || exit 1; cp "$TEST_ROOT/partition/"* "$4/"',
        "umount": 'echo unmounted >> "$TEST_ROOT/unmounts"',
    }
    for name, body in mocks.items():
        path = commands / name
        path.write_text("#!" + shutil.which("sh") + "\n" + body + "\n")
        path.chmod(0o700)

    def run(expected, device=None):
        result = subprocess.run(["bash", str(script), str(media), str(runtime), str(device or root / "no-device")],
                                env=environment, capture_output=True, text=True)
        assert result.returncode == expected, (result.returncode, result.stderr)
        assert "DO-NOT-LOG" not in result.stdout + result.stderr

    run(1)  # No answers: discovery must launch the ordinary interactive installer.
    answers = media / "floatlab-node.toml"
    answers.write_text("# DO-NOT-LOG\nversion=1\n")
    run(0)
    copied = runtime / "answers.toml"
    assert copied.read_bytes() == answers.read_bytes()
    assert copied.stat().st_mode & 0o777 == 0o600
    other = media / "floatlab-other.toml"
    other.write_text("version=1\n")
    run(2)  # Multiple files must never select an arbitrary destructive install.
    assert not copied.exists(), "ambiguous media retained a stale destructive answer file"
    other.unlink()
    answers.unlink()
    run(1)
    assert not copied.exists()

    partition = root / "partition"
    partition.mkdir()
    (partition / "floatlab-usb.toml").write_text("# DO-NOT-LOG\nversion=1\n")
    environment["TEST_PARENT"] = "testusb"
    run(0)  # A writable partition alongside the read-only USB ISO.
    assert copied.read_bytes() == (partition / "floatlab-usb.toml").read_bytes()
    assert (root / "unmounts").read_text().count("unmounted") == 1

    environment["TEST_PARENT"] = ""
    device = root / "answers-cd"
    device.touch()
    run(0, device)  # The exact discovery path used by disposable E2E hosts.
    assert copied.read_bytes() == (partition / "floatlab-usb.toml").read_bytes()
    assert (root / "unmounts").read_text().count("unmounted") == 2
print("installer answers discovery: passed")
