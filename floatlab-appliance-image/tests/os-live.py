"""Exercise the stage-1 clone lifecycle without touching a real pool."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(sys.argv.pop(1)) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "nixos/modules/os-live.sh"

ZFS = r'''#!{python}
import json, os, sys
from pathlib import Path
p = Path(os.environ["ZFS_TEST_STATE"])
s = json.loads(p.read_text())
args = sys.argv[1:]
s["commands"].append(args)
p.write_text(json.dumps(s))
if args[0] == s.get("fail"):
    sys.exit(1)
if args[0] == "get":
    prop, dataset = args[-2:]
    print(s["datasets"][dataset][prop])
elif args[0] == "list":
    sys.exit(0 if args[-1] in s["datasets"] else 1)
elif args[0] == "destroy":
    del s["datasets"][args[-1]]
elif args[0] == "clone":
    s["datasets"][args[-1]] = {{"origin": args[-2], "mounted": "no", "readonly": "off", "writes": []}}
else:
    sys.exit(1)
p.write_text(json.dumps(s))
'''


class LiveRoot(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        root = Path(self.temp.name)
        shim = root / "zfs"
        shim.write_text(ZFS.format(python=sys.executable))
        shim.chmod(0o755)
        self.state = root / "state.json"
        self.cmdline = root / "cmdline"
        self.cmdline.write_text("root=zfs:floatlab/os/current floatlab.installed=1 spl.spl_hostid=0x12345678 init=/nix/store/example/init")
        self.env = dict(os.environ, FLOATLAB_CMDLINE_FILE=str(self.cmdline), PATH=str(root) + ":" + os.environ["PATH"], ZFS_TEST_STATE=str(self.state))
        self.data = {"commands": [], "datasets": {
            "floatlab/os/current": {"origin": "floatlab/os/v1@base", "readonly": "on", "org.floatlab:host-id": "12345678", "org.floatlab:version": "v1", "org.floatlab:init": "/nix/store/example/init"},
            "floatlab/os/v1": {"readonly": "on", "org.floatlab:host-id": "12345678"},
            "floatlab/os/v1@base": {},
            "floatlab/os/live": {"mounted": "no", "writes": ["dirty"]},
            "floatlab/system/etc": {"writes": ["persistent"]},
        }}

    def boot(self, succeeds=True):
        self.state.write_text(json.dumps(self.data))
        result = subprocess.run(["sh", str(SCRIPT)], env=self.env, capture_output=True, text=True)
        self.data = json.loads(self.state.read_text())
        self.assertEqual(result.returncode == 0, succeeds, result.stderr)

    def test_two_boots_discard_writes_and_keep_persistent_state(self):
        pristine = self.data["datasets"]["floatlab/os/v1@base"].copy()
        for _ in range(2):
            self.boot()
            live = self.data["datasets"]["floatlab/os/live"]
            self.assertEqual(live["origin"], "floatlab/os/v1@base")
            self.assertEqual(live["writes"], [])
            self.assertEqual(self.data["datasets"]["floatlab/os/v1@base"], pristine)
            self.assertEqual(self.data["datasets"]["floatlab/system/etc"]["writes"], ["persistent"])
            live["writes"].append("new dirty write")

    def test_invalid_origin_or_mounted_live_is_never_destroyed(self):
        for origin, mounted in [("floatlab/system/etc@base", "no"), ("floatlab/os/current@base", "no"), ("floatlab/os/v1@base", "yes")]:
            with self.subTest(origin=origin, mounted=mounted):
                self.data["commands"] = []
                self.data["datasets"]["floatlab/os/current"]["origin"] = origin
                self.data["datasets"]["floatlab/os/live"]["mounted"] = mounted
                self.boot(False)
                self.assertFalse(any(c[0] == "destroy" for c in self.data["commands"]))

    def test_selected_trial_uses_candidate_origin(self):
        self.data["datasets"].update({"floatlab/os/next": {"origin": "floatlab/os/v2@base", "readonly": "on", "org.floatlab:host-id": "12345678", "org.floatlab:version": "v2", "org.floatlab:init": "/nix/store/example/init"}, "floatlab/os/v2": {"readonly": "on", "org.floatlab:host-id": "12345678"}, "floatlab/os/v2@base": {}})
        self.cmdline.write_text("root=zfs:floatlab/os/next floatlab.trial=operation spl.spl_hostid=0x12345678 init=/nix/store/example/init")
        self.boot()
        self.assertEqual(self.data["datasets"]["floatlab/os/live"]["origin"], "floatlab/os/v2@base")

    def test_missing_or_ambiguous_selected_root_fails_without_destruction(self):
        for line in ["", "root=zfs:floatlab/system/etc", "root=zfs:floatlab/os/current root=zfs:floatlab/os/next"]:
            self.cmdline.write_text(line)
            self.data["commands"] = []
            self.boot(False)
            self.assertFalse(any(c[0] == "destroy" for c in self.data["commands"]))

    def test_identity_or_init_mismatch_never_destroys_live(self):
        for key, value in [("org.floatlab:host-id", "87654321"), ("org.floatlab:init", "/nix/store/other/init"), ("org.floatlab:version", "v99")]:
            original = self.data["datasets"]["floatlab/os/current"][key]
            self.data["datasets"]["floatlab/os/current"][key] = value
            self.data["commands"] = []
            self.boot(False)
            self.assertFalse(any(c[0] == "destroy" for c in self.data["commands"]))
            self.data["datasets"]["floatlab/os/current"][key] = original

    def test_destroy_and_clone_failures_stop_boot(self):
        for command in ["destroy", "clone"]:
            with self.subTest(command=command):
                self.data["commands"] = []
                self.data["fail"] = command
                self.boot(False)
                self.assertIn("floatlab/os/current", self.data["datasets"])
                self.assertIn("floatlab/os/v1@base", self.data["datasets"])


if __name__ == "__main__":
    unittest.main()
