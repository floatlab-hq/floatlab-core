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
        self.env = dict(os.environ, PATH=str(root) + ":" + os.environ["PATH"], ZFS_TEST_STATE=str(self.state))
        self.data = {"commands": [], "datasets": {
            "floatlab/os/current": {"origin": "floatlab/os/v1@base", "readonly": "on"},
            "floatlab/os/v1": {"readonly": "on"},
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
