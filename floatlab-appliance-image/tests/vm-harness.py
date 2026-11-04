"""Check VM launchers use UEFI, blank disks and private installer answers media."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parents[2]
with tempfile.TemporaryDirectory() as temporary:
    work = Path(temporary)
    commands = work / "bin"
    commands.mkdir()
    log = work / "commands.jsonl"
    fake = f"#!{sys.executable}\n" + '''import json, os, pathlib, sys
name, args = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
with open(os.environ['HARNESS_LOG'], 'a') as log: log.write(json.dumps([name, *args]) + '\\n')
if name == 'virsh' and args[2] in ('dominfo', 'vol-info'):
    sys.exit(0 if os.environ.get('HARNESS_EXISTING') == '1' else 1)
if name == 'xorriso':
    source = pathlib.Path(args[-1]) / 'floatlab-answers.toml'
    assert source.read_bytes() == pathlib.Path(os.environ['FLOATLAB_ANSWERS']).read_bytes()
    destination = pathlib.Path(args[args.index('-o') + 1])
    destination.write_bytes(b'private-mock-ISO')
    assert destination.stat().st_mode & 0o777 == 0o600
if name == 'qemu-img': pathlib.Path(args[-2]).touch()
'''
    for name in ("virsh", "virt-install", "xorriso", "qemu-system-x86_64", "qemu-img"):
        path = commands / name
        path.write_text(fake)
        path.chmod(0o700)
    iso, answers = work / "release.iso", work / "floatlab-test.toml"
    iso.write_bytes(b"unchanged-release-ISO")
    answers.write_text("version = 1\n# hash-only answers fixture\n")
    firmware = work / "firmware"
    firmware.mkdir()
    for name in ("OVMF_CODE.fd", "OVMF_VARS.fd"):
        (firmware / name).touch()
    environment = {**os.environ, "PATH": str(commands) + ":" + os.environ["PATH"],
                   "HARNESS_LOG": str(log), "VM_NAME": "owned-test-host", "LIBVIRT_POOL": "owned-test-pool",
                   "LIBVIRT_URI": "test:///default", "FLOATLAB_ANSWERS": str(answers),
                   "FLOATLAB_APPLIANCE_ISO": str(iso), "FLOATLAB_OVMF": str(firmware),
                   "FLOATLAB_TEST_DISK": str(work / "blank.raw")}

    def run(script, expected=0, unattended=True):
        log.write_text("")
        env = environment.copy()
        if not unattended: env.pop("FLOATLAB_ANSWERS")
        result = subprocess.run(["bash", str(root / script), str(iso)], env=env, capture_output=True, text=True)
        assert result.returncode == expected, (script, result.returncode, result.stderr)
        entries = [json.loads(line) for line in log.read_text().splitlines()]
        for entry in entries:
            if entry[0] == "xorriso":
                assert "FLOATLAB_ANSWERS" in entry and "-R" in entry
                assert not Path(entry[-1]).exists(), "private answers staging directory leaked"
                assert not Path(entry[entry.index("-o") + 1]).exists(), "private answers ISO leaked"
        return entries

    for script in ("floatlab-appliance-image/scripts/run-libvirt.sh", "scripts/create-floatlab-vm.sh"):
        entries = run(script)
        launch = next(row for row in entries if row[0] == "virt-install")
        assert launch[launch.index("--boot") + 1].startswith("uefi")
        assert "--transient" not in launch
        assert any("serial=floatlab-zfs,boot_order=1" in value for value in launch)
        assert any("device=cdrom,readonly=on,boot_order=2" in value for value in launch)
        assert any("owned-test-host-answers.iso,device=cdrom,readonly=on" in value for value in launch)
        assert any(row[0] == "virsh" and "vol-upload" in row for row in entries)
    entries = run("floatlab-appliance-image/scripts/run-libvirt.sh", unattended=False)
    assert not any(row[0] == "xorriso" for row in entries), "interactive install unexpectedly used answers"
    environment["HARNESS_EXISTING"] = "1"
    entries = run("floatlab-appliance-image/scripts/run-libvirt.sh", expected=1)
    assert not any(row[0] in ("virt-install", "xorriso") or "vol-create-as" in row for row in entries)
    environment.pop("HARNESS_EXISTING")
    entries = run("floatlab-appliance-image/scripts/run-qemu.sh")
    launch = next(row for row in entries if row[0] == "qemu-system-x86_64")
    assert any("pflash" in value and "OVMF_CODE.fd" in value for value in launch)
    assert any("serial=floatlab-zfs" in value for value in launch)
    assert iso.read_bytes() == b"unchanged-release-ISO"
print("installer VM launchers: passed")
