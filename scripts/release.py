#!/usr/bin/env python3
"""Release metadata and offline bundles; registry access occurs only at build time."""
import argparse
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parent.parent
LOCK = ROOT / "releases/iso.lock.json"
DIGEST = re.compile(r"^sha256:[0-9a-f]{64}$")
VERSION = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$")


def run(*args, **kwargs):
    return subprocess.check_output(args, text=True, cwd=ROOT, **kwargs).strip()


def read(path):
    return json.loads(Path(path).read_text())


def write(path, value):
    Path(path).write_text(json.dumps(value, indent=2) + "\n")


def checksum(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def release_notes(tag):
    run("bash", "scripts/check-release-tag.sh", tag)
    component, version = tag.split("-v", 1)
    text = (ROOT / f"CHANGELOG-{component}.md").read_text()
    match = re.search(rf"^## {re.escape(version)}\s*\n(.*?)(?=^## |\Z)", text, re.M | re.S)
    if not match or not match[1].strip():
        raise ValueError(f"Missing changelog entry for {tag}")
    return match[1].strip() + "\n"


def download(repository, tag, name, directory):
    run("gh", "release", "download", tag, "--repo", repository,
        "--pattern", name, "--dir", str(directory), "--clobber")
    return Path(directory) / name


def registry_digest(reference):
    digest = run("docker", "buildx", "imagetools", "inspect", reference,
                 "--format", "{{.Manifest.Digest}}")
    if not DIGEST.fullmatch(digest):
        raise ValueError(f"Invalid registry digest for {reference}: {digest}")
    return digest


def image_info(reference):
    if reference != "floatlab-manage:dev":
        run("docker", "pull", "--platform", "linux/amd64", reference)
    info = json.loads(run("docker", "image", "inspect", reference))[0]
    if info["Architecture"] != "amd64" or info["Os"] != "linux":
        raise ValueError(f"Expected linux/amd64: {reference}")
    return info


def manage_manifest(version, commit, ghcr, dockerhub, directory):
    if not VERSION.fullmatch(version):
        raise ValueError("Invalid management version")
    registries = {}
    ids = []
    for name, reference in [("ghcr", ghcr), ("dockerhub", dockerhub)]:
        digest = registry_digest(reference)
        pinned = reference.rsplit(":", 1)[0] + "@" + digest
        ids.append(image_info(pinned)["Id"])
        registries[name] = {"reference": reference, "digest": digest}
    if ids[0] != ids[1]:
        raise ValueError("Registries contain different management images")
    value = {"version": version, "git_commit": commit, "architecture": "amd64",
             "required_host": read(ROOT / "pkg/release/compatibility.json"),
             "image_id": ids[0], "registries": registries}
    compatibility = read(ROOT / "releases/rqlite-compatibility.json")
    if not compatibility["rqlite_sources"] or not all(
            VERSION.fullmatch(v) for v in [compatibility["rqlite_version"], *compatibility["rqlite_sources"]]):
        raise ValueError("Explicit tested rqlite versions required")
    images = {"floatlab-control": ghcr.rsplit(":", 1)[0] + "@" + registries["ghcr"]["digest"]}
    for service, reference in read(LOCK)["sidecars"].items():
        if "@" not in reference:
            reference += "@" + registry_digest(reference)
        if not DIGEST.fullmatch(reference.split("@", 1)[1]):
            raise ValueError(f"Unpinned management sidecar: {service}")
        images[service] = reference
    rqlite_tag = images["rqlite"].split("@", 1)[0].rsplit(":", 1)[-1]
    if rqlite_tag != compatibility["rqlite_version"]:
        raise ValueError("rqlite compatibility policy does not match target image tag")
    value.update(compatibility, images=images)
    write(Path(directory) / "manage-manifest.json", value)


def validate_lock(lock):
    if lock.get("architecture") != "amd64":
        raise ValueError("Only amd64 ISO releases are supported")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", lock.get("repository", "")):
        raise ValueError("Invalid GitHub repository")
    if not lock.get("host") or not lock.get("manage"):
        raise ValueError("Release host/manage first, then run release.py lock")
    for component in ["host", "manage"]:
        entry = lock[component]
        manifest = entry["manifest"]
        if entry["tag"] != component + "-v" + manifest["version"]:
            raise ValueError(f"{component} tag/version mismatch")
        run("bash", "scripts/check-release-tag.sh", entry["tag"])
        if manifest["architecture"] != "amd64":
            raise ValueError(f"Wrong {component} architecture")
    host = lock["host"]["manifest"]
    if set(host["files"]) != {"floatlab-root.tar.gz", "zfsbootmenu.EFI"}:
        raise ValueError("Invalid host artifact list")
    for value in host["files"].values():
        if not re.fullmatch(r"[0-9a-f]{64}", value):
            raise ValueError("Invalid host artifact checksum")
    if not re.fullmatch(r"[0-9a-f]{40}", host["nixpkgs_rev"]):
        raise ValueError("Host must pin its Nixpkgs revision")
    manage = lock["manage"]["manifest"]
    required = manage["required_host"]
    run("go", "run", "./scripts/releasecheck", host["version"],
        required["min_host_version"], required["max_host_version"])
    ghcr = manage["registries"]["ghcr"]
    if not ghcr["reference"].startswith("ghcr.io/") or not DIGEST.fullmatch(ghcr["digest"]):
        raise ValueError("Management must pin a GHCR image digest")
    if set(lock["sidecars"]) != {"rqlite", "victoria-metrics", "victoria-logs"}:
        raise ValueError("Expected rqlite, metrics and logs sidecars")
    for reference in lock["sidecars"].values():
        if "@" not in reference or not DIGEST.fullmatch(reference.split("@", 1)[1]):
            raise ValueError(f"Unpinned sidecar: {reference}")


def update_lock(repository, host, manage):
    lock = read(LOCK)
    lock["repository"] = repository
    with tempfile.TemporaryDirectory() as directory:
        for component, tag in [("host", host), ("manage", manage)]:
            manifest = read(download(repository, tag, component + "-manifest.json", directory))
            lock[component] = {"tag": tag, "manifest": manifest}
    for service, reference in lock["sidecars"].items():
        if "@" not in reference:
            lock["sidecars"][service] = reference + "@" + registry_digest(reference)
    validate_lock(lock)
    write(LOCK, lock)


def container_bundle(directory, management, sidecars, expected_manage_id=None):
    images = []
    refs = {"floatlab-control": management, **sidecars}
    compose = (ROOT / "floatlab-appliance-image/files/core-stack/docker-compose.yml").read_text()
    for service, reference in refs.items():
        info = image_info(reference)
        if service == "floatlab-control" and expected_manage_id and info["Id"] != expected_manage_id:
            raise ValueError("Management image does not match release metadata")
        # Docker save/load does not retain RepoDigests; an ID-derived tag survives offline loading.
        local = "floatlab-bundle/" + service + ":" + info["Id"].split(":")[1]
        run("docker", "tag", reference, local)
        archive = service + ".tar"
        run("docker", "save", "--output", str(directory / "images" / archive), local)
        images.append({"archive": archive, "image": local, "id": info["Id"], "source": reference})
        pattern = rf"(^  {re.escape(service)}:\n(?:.*\n)*?    image: )[^\n]+"
        compose, count = re.subn(pattern, lambda m: m[1] + local, compose, count=1, flags=re.M)
        if count != 1:
            raise ValueError(f"Missing Compose image for {service}")
    (directory / "docker-compose.yml").write_text(compose)
    write(directory / "images.json", images)


def finish_bundle(directory, host, iso_version, manage_version):
    files = ["floatlab-root.tar.gz", "zfsbootmenu.EFI", "docker-compose.yml", "images.json"]
    files += ["images/" + p.name for p in sorted((directory / "images").glob("*.tar"))]
    manifest = {**host, "iso_version": iso_version, "manage_version": manage_version,
                "files": {name: checksum(directory / name) for name in files},
                "bytes": sum((directory / name).stat().st_size for name in files)}
    write(directory / "floatlab-manifest.json", manifest)


def prepare_iso(directory, iso_version, local_host=None):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / "images").mkdir(exist_ok=True)
    if local_host:
        host = read(local_host / "host-manifest.json")
        for name in host["files"]:
            shutil.copyfile(local_host / name, directory / name)
        management = "floatlab-manage:dev"
        run("docker", "build", "--tag", management, "--file", "docker/floatlab-control.Dockerfile", ".")
        sidecars = read(LOCK)["sidecars"]
        container_bundle(directory, management, sidecars)
        manage_version = "0.1.0-dev"
    else:
        lock = read(LOCK)
        validate_lock(lock)
        host = lock["host"]["manifest"]
        # Compare downloaded metadata too, so replacement release assets cannot silently alter a lock.
        for component in ["host", "manage"]:
            downloaded = read(download(lock["repository"], lock[component]["tag"], component + "-manifest.json", directory))
            if downloaded != lock[component]["manifest"]:
                raise ValueError(f"Published {component} metadata differs from ISO lock")
        for name, expected in host["files"].items():
            artifact = download(lock["repository"], lock["host"]["tag"], name, directory)
            if checksum(artifact) != expected:
                raise ValueError(f"Checksum mismatch: {name}")
        manage = lock["manage"]["manifest"]
        image = manage["registries"]["ghcr"]
        reference = image["reference"].rsplit(":", 1)[0] + "@" + image["digest"]
        container_bundle(directory, reference, lock["sidecars"], manage["image_id"])
        manage_version = manage["version"]
        notes = f"Host {host['version']}; management {manage_version}.\n\n"
        notes += "\n".join(f"- {s}: `{r}`" for s, r in {"management": reference, **lock["sidecars"]}.items()) + "\n"
        (directory / "release-notes.md").write_text(notes)
    finish_bundle(directory, host, iso_version, manage_version)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    notes = sub.add_parser("notes")
    notes.add_argument("tag")
    lock = sub.add_parser("lock")
    lock.add_argument("--repository", default=read(LOCK)["repository"])
    lock.add_argument("--host", required=True)
    lock.add_argument("--manage", required=True)
    manifest = sub.add_parser("manage-manifest")
    for name in ["version", "commit", "ghcr", "dockerhub", "directory"]:
        manifest.add_argument(name)
    prepare = sub.add_parser("prepare-iso")
    prepare.add_argument("directory", type=Path)
    prepare.add_argument("--version", required=True)
    prepare.add_argument("--local-host", type=Path)
    sub.add_parser("validate-lock")
    args = parser.parse_args()
    if args.command == "notes":
        print(release_notes(args.tag), end="")
    elif args.command == "lock":
        update_lock(args.repository, args.host, args.manage)
    elif args.command == "manage-manifest":
        manage_manifest(args.version, args.commit, args.ghcr, args.dockerhub, args.directory)
    elif args.command == "prepare-iso":
        prepare_iso(args.directory.resolve(), args.version, args.local_host)
    else:
        validate_lock(read(LOCK))


if __name__ == "__main__":
    main()
