import importlib.util
import hashlib
import io
import json
from pathlib import Path
import shutil
import subprocess
import tempfile
import tarfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release", Path(__file__).with_name("release.py"))
release = importlib.util.module_from_spec(spec)
spec.loader.exec_module(release)


def image_archive(path, tag, architecture="amd64"):
    config = json.dumps({"architecture": architecture, "os": "linux", "config": {}}).encode()
    digest = hashlib.sha256(config).hexdigest()
    config_path = "blobs/sha256/" + digest
    manifest = json.dumps([{"Config": config_path, "RepoTags": [tag], "Layers": []}]).encode()
    with tarfile.open(path, "w") as archive:
        for name, data in [("manifest.json", manifest), (config_path, config)]:
            member = tarfile.TarInfo(name)
            member.size = len(data)
            archive.addfile(member, io.BytesIO(data))
    return "sha256:" + digest


def bundle_fixture(directory):
    (directory / "images").mkdir()
    images = []
    for service in ["floatlab-control", "rqlite", "victoria-metrics", "victoria-logs"]:
        tag = "floatlab-bundle/" + service + ":test"
        archive = service + ".tar"
        digest = image_archive(directory / "images" / archive, tag)
        images.append({"archive": archive, "image": tag, "id": digest})
    release.write(directory / "images.json", images)
    (directory / "docker-compose.yml").write_text("services:\n" + "".join(
        f"  {image['archive'][:-4]}:\n    image: {image['image']}\n" for image in images))
    return images


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
    def test_archive_requires_expected_tag_and_platform(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "image.tar"
            image_archive(path, "example:test")
            with self.assertRaisesRegex(ValueError, "tagged"):
                release.archive_image_id(path, "other:test")
            image_archive(path, "example:test", architecture="arm64")
            with self.assertRaisesRegex(ValueError, "linux/amd64"):
                release.archive_image_id(path, "example:test")

    def test_bundle_rejects_image_store_digest_instead_of_saved_config_id(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            images = bundle_fixture(directory)
            release.verify_bundle(directory)
            images[0]["id"] = "sha256:" + "a" * 64
            release.write(directory / "images.json", images)
            with self.assertRaisesRegex(ValueError, "ID mismatch for floatlab-control.tar"):
                release.verify_bundle(directory)

    def test_bundle_rejects_missing_images_and_compose_mismatches(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            images = bundle_fixture(directory)
            release.write(directory / "images.json", images[:-1])
            with self.assertRaisesRegex(ValueError, "four core"):
                release.verify_bundle(directory)
            release.write(directory / "images.json", images)
            (directory / "docker-compose.yml").write_text("services: {}\n")
            with self.assertRaisesRegex(ValueError, "Compose does not reference"):
                release.verify_bundle(directory)

    def test_iso_verifies_extracted_archives_not_build_directory(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            images = bundle_fixture(directory)

            def extract(*args):
                self.assertEqual(args[:5], ("xorriso", "-osirrox", "on", "-indev", "/finished.iso"))
                for i in range(5, len(args), 3):
                    self.assertEqual(args[i], "-extract")
                    source = directory / args[i + 1].lstrip("/")
                    target = Path(args[i + 2])
                    if source.is_dir():
                        shutil.copytree(source, target)
                    else:
                        shutil.copyfile(source, target)
                return ""

            with patch.object(release, "run", side_effect=extract):
                release.verify_iso(Path("/finished.iso"))
                images[0]["id"] = "sha256:" + "a" * 64
                release.write(directory / "images.json", images)
                with self.assertRaisesRegex(ValueError, "ID mismatch"):
                    release.verify_iso(Path("/finished.iso"))

    def test_management_workflow_checksums(self):
        workflow = (release.ROOT / ".github/workflows/release-manage.yml").read_text()
        command, = [line.strip() for line in workflow.splitlines()
                    if line.lstrip().startswith("sha256sum ")]
        names = {"lab-linux-x64", "lab-linux-arm64", "lab-darwin-x64", "lab-darwin-arm64", "manage-manifest.json"}
        with tempfile.TemporaryDirectory() as directory:
            for name in names:
                (Path(directory) / name).write_text(name)
            subprocess.run(command, shell=True, cwd=directory, check=True, capture_output=True)
            checksums = (Path(directory) / "SHA256SUMS").read_text()
            self.assertEqual({line.split("  ", 1)[1] for line in checksums.splitlines()}, names)
            subprocess.run(["sha256sum", "-c", "SHA256SUMS"], cwd=directory, check=True, capture_output=True)

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
            self.assertEqual(set(metadata["images"]), {"floatlab-control", "rqlite", "victoria-metrics", "victoria-logs"})
            self.assertTrue(all(release.DIGEST.fullmatch(ref.split("@", 1)[1]) for ref in metadata["images"].values()))
            self.assertEqual(metadata["rqlite_version"], "8.26.0")
            self.assertEqual(metadata["rqlite_sources"], ["8.26.0"])
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(release, "registry_digest", return_value="sha256:" + "a" * 64), \
                patch.object(release, "image_info", side_effect=[{"Id": "a"}, {"Id": "b"}]):
            with self.assertRaisesRegex(ValueError, "different management images"):
                release.manage_manifest("0.1.0", "commit", "ghcr.io/x:0.1.0", "docker.io/x:0.1.0", directory)
            self.assertFalse((Path(directory) / "manage-manifest.json").exists())

    def test_container_bundle_and_checksums(self):
        def docker(*args):
            if args[:2] == ("docker", "save"):
                image_archive(Path(args[3]), args[4])
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
                self.assertNotEqual(image["id"], "sha256:" + "a" * 64)
                self.assertEqual(image["id"], release.archive_image_id(directory / "images" / image["archive"], image["image"]))
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
