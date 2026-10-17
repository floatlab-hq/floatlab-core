"""Exercise dev tooling with isolated files and fake host commands; never touch the host."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

REPO = Path(__file__).resolve().parents[1]


class DevWorkflow(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.system = self.root / "system"
        (self.system / "etc").mkdir(parents=True)
        (self.root / "workspace").mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "commands"
        self.env = {**os.environ, "PATH": f"{self.bin}:{os.environ['PATH']}", "LOG": str(self.log)}

    def fake(self, name, body):
        path = self.bin / name
        path.write_text('#!/usr/bin/env bash\nset -eu\n' + body + '\n')
        path.chmod(0o755)

    def run_script(self, name, *args, **env):
        source = (REPO / name).read_text().replace("/floatlab/system", str(self.system))
        source = source.replace("/workspace", str(self.root / "workspace"))
        source = source.replace("/run/floatlab-mode.lock", str(self.root / "mode.lock"))
        return subprocess.run(["bash", "-c", source, name, *args], env={**self.env, **env}, text=True, capture_output=True)

    def test_modes_preserve_credentials_and_preflight_image(self):
        self.fake("id", 'echo 0')
        self.fake("docker", 'echo "docker $*" >> "$LOG"; exit "${IMAGE_STATUS:-0}"')
        self.fake("systemctl", 'echo "systemctl $*" >> "$LOG"')
        self.fake("floatlab-start-core-stack", 'echo stack >> "$LOG"')
        mode = "scripts/floatlab-mode.sh"
        marker = self.system / "etc/development"
        self.assertEqual(self.run_script(mode).stdout.strip(), "production")
        self.assertNotEqual(self.run_script(mode, "dev", IMAGE_STATUS="1").returncode, 0)
        self.assertFalse(marker.exists())
        self.assertEqual(self.run_script(mode, "dev").returncode, 0)
        self.assertEqual(self.run_script(mode).stdout.strip(), "dev")
        home = self.system / "dev/home"
        self.assertEqual(home.stat().st_mode & 0o777, 0o700)
        credential = home / "credential"
        credential.write_text("persist me")
        self.assertEqual(self.run_script(mode, "production").returncode, 0)
        self.assertFalse(marker.exists())
        self.assertEqual(credential.read_text(), "persist me")
        self.assertNotEqual(self.run_script(mode, "invalid").returncode, 0)
        self.assertEqual(self.log.read_text().count("systemctl restart floatlab-hostd"), 2)

    def test_hostd_only_replaces_binary_after_successful_build(self):
        deploy = "docker/dev/hostd.sh"
        self.fake("go", 'if [ "${FAIL_BUILD:-0}" = 1 ]; then exit 1; fi; printf new > "$3"')
        self.assertNotEqual(self.run_script(deploy).returncode, 0)
        (self.system / "etc/development").touch()
        binary = self.system / "dev/bin/floatlab-hostd"
        binary.parent.mkdir(parents=True)
        binary.write_text("old")
        inode = binary.stat().st_ino
        self.assertNotEqual(self.run_script(deploy, FAIL_BUILD="1").returncode, 0)
        self.assertEqual(binary.read_text(), "old")
        self.assertEqual(self.run_script(deploy).returncode, 0)
        self.assertEqual(binary.read_text(), "new")
        self.assertNotEqual(binary.stat().st_ino, inode)
        self.assertEqual(binary.stat().st_mode & 0o777, 0o755)
        self.assertEqual(list(binary.parent.glob(".hostd.*")), [])

    def test_pr_requires_reviewed_commits_and_pushes_without_force(self):
        self.fake("git", '''case "$*" in
            'status --porcelain') printf '%s' "${DIRTY:-}";;
            'symbolic-ref --short HEAD') echo "${BRANCH:-codex/test}";;
            *) echo "git $*" >> "$LOG";;
        esac''')
        self.fake("gh", 'echo "gh $*" >> "$LOG"')
        helper = "docker/dev/pr.sh"
        self.assertNotEqual(self.run_script(helper).returncode, 0)
        self.assertNotEqual(self.run_script(helper, "Title", DIRTY="modified").returncode, 0)
        self.assertNotEqual(self.run_script(helper, "Title", BRANCH="main").returncode, 0)
        self.assertEqual(self.run_script(helper, "Title").returncode, 0)
        commands = self.log.read_text()
        self.assertEqual(commands.count("git push"), 1)
        self.assertIn("git push --set-upstream origin codex/test", commands)
        self.assertIn("gh pr create --draft --fill --title Title", commands)

    def test_pr_offers_device_login_when_not_authenticated(self):
        self.fake("git", '''case "$*" in
            'status --porcelain') ;;
            'symbolic-ref --short HEAD') echo codex/test;;
        esac''')
        self.fake("gh", '''echo "gh $*" >> "$LOG"
            if [ "$*" = 'auth status' ]; then exit 1; fi''')
        self.assertEqual(self.run_script("docker/dev/pr.sh", "Title").returncode, 0)
        self.assertIn("gh auth login --hostname github.com --git-protocol https --web", self.log.read_text())

    def test_compose_keeps_installed_identity_and_data(self):
        base = REPO / "floatlab-appliance-image/files/core-stack/docker-compose.yml"
        override = base.with_name("docker-compose.dev.yml")
        result = subprocess.run(["docker", "compose", "-f", str(base), "-f", str(override), "config", "--format", "json"], check=True, capture_output=True, text=True)
        control = json.loads(result.stdout)["services"]["floatlab-control"]
        self.assertEqual(control["image"], "floatlab-dev:local")
        self.assertEqual(control["environment"]["FLOATLAB_DEV_MODE"], "true")
        self.assertIn("FLOATLAB_APPLIANCE_CONFIG", control["environment"])
        self.assertNotIn("FLOATLAB_BOOTSTRAP_PASSWORD", control["environment"])
        mounts = {mount["target"]: mount["source"] for mount in control["volumes"]}
        self.assertEqual(mounts["/var/lib/floatlab/raft"], "/floatlab/system/raft")
        self.assertEqual(mounts["/root"], "/floatlab/system/dev/home")
        editor = next(port for port in control["ports"] if port["target"] == 9090)
        self.assertEqual(editor["host_ip"], "127.0.0.1")


if __name__ == "__main__":
    unittest.main()
