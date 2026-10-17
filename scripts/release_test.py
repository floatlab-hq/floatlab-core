import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def lock_fixture():
    return {
        "architecture": "amd64", "repository": "floatlab-hq/floatlab-core",
        "host": {"tag": "host-v0.1.0", "manifest": {
            "version": "0.1.0", "architecture": "amd64", "nixpkgs_rev": "a" * 40,
            "files": {"floatlab-root.tar.gz": "a" * 64, "zfsbootmenu.EFI": "b" * 64}}},
        "manage": {"tag": "manage-v0.1.0", "manifest": {
            "version": "0.1.0", "architecture": "amd64", "required_host": {
                "min_host_version": "0.1.0", "max_host_version": "0.2.0"},
            "registries": {"ghcr": {"reference": "ghcr.io/floatlab-hq/floatlab-manage:0.1.0",
                                    "digest": "sha256:" + "c" * 64}}}},
        "sidecars": {name: "example/" + name + "@sha256:" + "d" * 64
                     for name in ["rqlite", "victoria-metrics", "victoria-logs"]},
    }


class ReleaseTests(unittest.TestCase):
    def test_lock_validation(self):
        with patch.object(release, "run") as run:
            release.validate_lock(lock_fixture())
            run.assert_any_call("go", "run", "./scripts/releasecheck", "0.1.0", "0.1.0", "0.2.0")
            for mutate in [
                lambda lock: lock.update(host=None),
                lambda lock: lock.update(architecture="arm64"),
                lambda lock: lock["host"]["manifest"].update(architecture="arm64"),
                lambda lock: lock["host"].update(tag="host-v0.2.0"),
                lambda lock: lock["host"]["manifest"]["files"].update({"../escape": "a" * 64}),
                lambda lock: lock["sidecars"].update(rqlite="rqlite:latest"),
            ]:
                lock = lock_fixture()
                mutate(lock)
                with self.assertRaises(ValueError):
                    release.validate_lock(lock)

    def test_registry_manifests_are_checked_independently(self):
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(release, "registry_digest", side_effect=["sha256:" + "a" * 64, "sha256:" + "b" * 64]), \
                patch.object(release, "image_info", return_value={"Id": "sha256:" + "c" * 64}):
            release.manage_manifest("0.1.0", "commit", "ghcr.io/x:0.1.0", "docker.io/x:0.1.0", directory)
            metadata = release.read(Path(directory) / "manage-manifest.json")
            self.assertNotEqual(metadata["registries"]["ghcr"]["digest"], metadata["registries"]["dockerhub"]["digest"])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(release, "registry_digest", return_value="sha256:" + "a" * 64), \
                patch.object(release, "image_info", side_effect=[{"Id": "a"}, {"Id": "b"}]):
            with self.assertRaisesRegex(ValueError, "different management images"):
                release.manage_manifest("0.1.0", "commit", "ghcr.io/x:0.1.0", "docker.io/x:0.1.0", directory)
            self.assertFalse((Path(directory) / "manage-manifest.json").exists())

    def test_container_bundle_and_checksums(self):
        def docker(*args):
            if args[:2] == ("docker", "save"):
                Path(args[3]).write_bytes(b"image archive")
            return ""

        with tempfile.TemporaryDirectory() as directory, \
                patch.object(release, "run", side_effect=docker), \
                patch.object(release, "image_info", return_value={"Id": "sha256:" + "a" * 64}):
            directory = Path(directory)
            (directory / "images").mkdir()
            release.container_bundle(directory, "management@digest", lock_fixture()["sidecars"])
            images = release.read(directory / "images.json")
            self.assertEqual(len(images), 4)
            compose = (directory / "docker-compose.yml").read_text()
            self.assertNotIn("floatlab-control:local", compose)
            for image in images:
                self.assertIn("image: " + image["image"], compose)
            for name in ["floatlab-root.tar.gz", "zfsbootmenu.EFI"]:
                (directory / name).write_bytes(b"host")
            release.finish_bundle(directory, {"version": "0.1.0"}, "0.2.0", "0.3.0")
            manifest = release.read(directory / "floatlab-manifest.json")
            self.assertEqual(manifest["version"], "0.1.0")
            self.assertEqual(manifest["iso_version"], "0.2.0")
            self.assertEqual(manifest["manage_version"], "0.3.0")
            for name, digest in manifest["files"].items():
                self.assertEqual(release.checksum(directory / name), digest)

    def test_changelog_entry(self):
        self.assertIn("Publish", release.release_notes("manage-v0.1.0"))
        with self.assertRaises(ValueError):
            release.release_notes("host-v99.0.0")

    def test_iso_rejects_corrupt_host_before_loading_containers(self):
        lock = lock_fixture()

        def download(repository, tag, name, directory):
            target = Path(directory) / name
            if name.endswith("manifest.json"):
                component = name.split("-")[0]
                release.write(target, lock[component]["manifest"])
            else:
                target.write_bytes(b"corrupt host")
            return target

        original_read = release.read
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(release, "read", side_effect=lambda path: lock if path == release.LOCK else original_read(path)), \
                patch.object(release, "run"), \
                patch.object(release, "download", side_effect=download), \
                patch.object(release, "container_bundle") as containers:
            with self.assertRaisesRegex(ValueError, "Checksum mismatch"):
                release.prepare_iso(Path(directory), "0.1.0")
            containers.assert_not_called()


if __name__ == "__main__":
    unittest.main()
