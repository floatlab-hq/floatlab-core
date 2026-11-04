"""Check the appliance ARC budget without writing host kernel parameters."""
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(sys.argv.pop(1)) if len(sys.argv) > 1 else Path(__file__).resolve().parents[1] / "nixos/modules/zfs-memory.sh"
GIB = 1 << 30


class ARCBudget(unittest.TestCase):
    def budget(self, memory, default, explicit=0):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "meminfo").write_text(f"MemTotal: {memory // 1024} kB\n")
            (root / "arcstats").write_text(f"c_max 4 {default}\n")
            (root / "arc_max").write_text(str(explicit) + "\n")
            env = dict(os.environ, FLOATLAB_MEMINFO_FILE=str(root / "meminfo"), FLOATLAB_ARC_STATS_FILE=str(root / "arcstats"), FLOATLAB_ARC_MAX_FILE=str(root / "arc_max"))
            result = subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            return int((root / "arc_max").read_text())

    def test_minimum_host_leaves_room_for_production_and_update(self):
        for memory in (3584 << 20, 4 * GIB):
            self.assertEqual(self.budget(memory, 3 * GIB), memory - 3 * GIB)

    def test_medium_host_reserves_three_gib(self):
        self.assertEqual(self.budget(8 * GIB, 6 * GIB), 5 * GIB)

    def test_larger_host_keeps_default(self):
        self.assertEqual(self.budget(16 * GIB, 12 * GIB), 0)

    def test_explicit_administrator_limits_are_preserved(self):
        for limit in (256 << 20, 2 * GIB):
            self.assertEqual(self.budget(4 * GIB, 3 * GIB, limit), limit)


if __name__ == "__main__":
    unittest.main()
