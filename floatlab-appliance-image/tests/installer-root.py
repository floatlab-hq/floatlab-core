"""Check installer clone ownership and cleanup without mounting real filesystems."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(sys.argv.pop(1)) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "nixos/modules/installer-root.sh"
SHIM = r'''#!{python}
import json, os, sys
from pathlib import Path
state = Path(os.environ["INSTALLER_ROOT_STATE"])
s = json.loads(state.read_text())
command = [Path(sys.argv[0]).name] + sys.argv[1:]
s["commands"].append(command)
state.write_text(json.dumps(s))
if command[:2] == s.get("fail"):
    sys.exit(1)
name, args = command[0], command[1:]
if name == "zfs":
    dataset = args[-1]
    if args[0] == "get":
        print(s["datasets"][dataset][args[-2]])
    elif args[0] == "list":
        sys.exit(0 if dataset in s["datasets"] else 1)
    elif args[0] == "clone":
        s["datasets"][dataset] = {{"origin": args[-2], "org.floatlab:installer-runtime": os.environ["INVOCATION_ID"], "mounted": "no"}}
    elif args[0] == "destroy":
        del s["datasets"][dataset]
elif name == "mount" and args[0] == "-t":
    s["datasets"]["floatlab/os/installer"]["mounted"] = "yes"
elif name == "mount" and args[0] == "--bind":
    s["runtime_mounted"] = True
elif name == "mountpoint":
    mounted = s.get("runtime_mounted", False) if args[-1] == "/run/floatlab-installer" else s["datasets"].get("floatlab/os/installer", {{}}).get("mounted") == "yes"
    sys.exit(0 if mounted else 1)
elif name == "umount":
    if args[-1] == "/run/floatlab-installer":
        s["runtime_mounted"] = False
    else:
        s["datasets"]["floatlab/os/installer"]["mounted"] = "no"
state.write_text(json.dumps(s))
'''


class InstallerRoot(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        directory = Path(temp.name)
        for name in ["zfs", "mount", "mountpoint", "umount", "chroot", "mkdir", "rm", "cp", "ln", "touch"]:
            shim = directory / name
            shim.write_text(SHIM.format(python=sys.executable))
            shim.chmod(0o755)
        self.state = directory / "state.json"
        self.env = dict(os.environ, PATH=str(directory) + ":" + os.environ["PATH"], INSTALLER_ROOT_STATE=str(self.state), INVOCATION_ID="test-invocation")
        self.data = {"commands": [], "datasets": {
            "floatlab/os/current": {"origin": "floatlab/os/v1@base"},
            "floatlab/os/v1": {"readonly": "on"},
        }}

    def run_script(self, action, succeeds=True):
        self.state.write_text(json.dumps(self.data))
        result = subprocess.run(["bash", str(SCRIPT), action], env=self.env, capture_output=True, text=True)
        self.data = json.loads(self.state.read_text())
        self.assertEqual(result.returncode == 0, succeeds, result.stderr)

    def test_prepare_and_cleanup_leave_pristine_root_untouched(self):
        self.run_script("prepare")
        self.assertEqual(self.data["datasets"]["floatlab/os/installer"]["origin"], "floatlab/os/v1@base")
        self.assertIn(["mount", "--make-rslave", "/run/floatlab-installer/root/floatlab"], self.data["commands"])
        private = self.data["commands"].index(["mount", "--make-private", "/run/floatlab-installer"])
        clone = next(i for i, c in enumerate(self.data["commands"]) if c[:2] == ["zfs", "clone"])
        self.assertLess(private, clone)
        self.run_script("cleanup")
        self.assertNotIn("floatlab/os/installer", self.data["datasets"])
        self.assertFalse(self.data["runtime_mounted"])
        self.assertEqual(self.data["datasets"]["floatlab/os/v1"], {"readonly": "on"})
        self.run_script("cleanup")

    def test_invalid_origin_and_existing_clone_are_not_destroyed(self):
        self.data["datasets"]["floatlab/os/current"]["origin"] = "floatlab/system/etc@base"
        self.run_script("prepare", False)
        self.assertFalse(any(c[:2] == ["zfs", "clone"] for c in self.data["commands"]))
        self.data["datasets"]["floatlab/os/current"]["origin"] = "floatlab/os/v1@base"
        self.run_script("prepare")
        self.run_script("prepare", False)
        self.assertFalse(any(c[:2] == ["zfs", "destroy"] for c in self.data["commands"]))
        self.env["INVOCATION_ID"] = "another-invocation"
        self.run_script("cleanup", False)
        self.assertFalse(any(c[:2] == ["zfs", "destroy"] for c in self.data["commands"]))

    def test_failed_unmount_or_wrong_owner_prevents_destroy(self):
        self.run_script("prepare")
        self.data["fail"] = ["umount", "--recursive"]
        self.run_script("cleanup", False)
        self.assertIn("floatlab/os/installer", self.data["datasets"])
        del self.data["fail"]
        self.data["datasets"]["floatlab/os/installer"]["org.floatlab:installer-runtime"] = "off"
        self.run_script("cleanup", False)
        self.assertFalse(any(c[:2] == ["zfs", "destroy"] for c in self.data["commands"]))

    def test_partial_setup_can_be_cleaned_up(self):
        self.data["fail"] = ["mount", "--rbind"]
        self.run_script("prepare", False)
        self.run_script("cleanup")
        self.assertNotIn("floatlab/os/installer", self.data["datasets"])

    def test_failed_destroy_keeps_the_private_mount_for_inspection(self):
        self.run_script("prepare")
        self.data["fail"] = ["zfs", "destroy"]
        self.run_script("cleanup", False)
        destroys = [c for c in self.data["commands"] if c[:2] == ["zfs", "destroy"]]
        self.assertEqual(destroys, [["zfs", "destroy", "floatlab/os/installer"]])
        self.assertTrue(self.data["runtime_mounted"])


if __name__ == "__main__":
    unittest.main()
