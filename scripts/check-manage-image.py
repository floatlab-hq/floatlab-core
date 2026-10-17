#!/usr/bin/env python3
"""Check the release container serves the real UI, assets and versioned API."""
import argparse
from html.parser import HTMLParser
import json
import subprocess
import time
import urllib.error
import urllib.request
import uuid


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


class Assets(HTMLParser):
    def __init__(self):
        super().__init__()
        self.paths = []

    def handle_starttag(self, tag, attrs):
        attrs = dict(attrs)
        if tag == "script" and attrs.get("src"):
            self.paths.append((attrs["src"], "javascript"))
        if tag == "link" and attrs.get("rel") == "stylesheet":
            self.paths.append((attrs["href"], "text/css"))


def check(image, version):
    name = "floatlab-image-check-" + uuid.uuid4().hex[:12]
    database, management = name + "-db", name + "-manage"
    docker("network", "create", name)
    try:
        docker("run", "--detach", "--name", database, "--network", name,
               "--network-alias", "rqlite", "rqlite/rqlite:8.26.0",
               "-http-addr=0.0.0.0:4001", "-raft-addr=0.0.0.0:4002")
        for _ in range(60):
            result = subprocess.run(["docker", "exec", database, "wget", "-qO-", "http://127.0.0.1:4001/readyz"], capture_output=True)
            if result.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Smoke-test rqlite did not become ready")
        docker("run", "--detach", "--name", management, "--network", name,
               "--publish", "127.0.0.1::8080", image,
               "--rqlite-url=http://rqlite:4001", "--raft-data=/tmp/raft",
               "--raft-bootstrap=true", "--jwt-secret=container-smoke-test")
        base = "http://" + docker("port", management, "8080/tcp")
        for _ in range(60):
            try:
                with urllib.request.urlopen(base + "/api/v1/health", timeout=2) as response:
                    health = json.load(response)
                break
            except (OSError, urllib.error.URLError):
                time.sleep(1)
        else:
            raise RuntimeError("Management container did not become healthy")
        assert health["version"] == version, health
        with urllib.request.urlopen(base + "/", timeout=5) as response:
            assert response.status == 200 and "text/html" in response.headers["Content-Type"]
            html = response.read().decode()
        assert 'id="app"' in html, "UI application root is missing"
        assets = Assets()
        assets.feed(html)
        assets.paths = [(path, kind) for path, kind in assets.paths
                        if path.startswith("/") and not path.startswith("//")]
        assert any(kind == "javascript" for _, kind in assets.paths), "No UI JavaScript"
        assert any(kind == "text/css" for _, kind in assets.paths), "No UI stylesheet"
        for path, kind in assets.paths:
            with urllib.request.urlopen(base + path, timeout=5) as response:
                assert response.status == 200 and kind in response.headers["Content-Type"], path
                assert response.read(), path
        print(f"PASS: {image} serves UI, {len(assets.paths)} JS/CSS assets and management {version}")
    except Exception:
        subprocess.run(["docker", "logs", management], check=False)
        raise
    finally:
        subprocess.run(["docker", "rm", "--force", "--volumes", management, database], capture_output=True)
        docker("network", "rm", name)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("image")
    parser.add_argument("version")
    args = parser.parse_args()
    check(args.image, args.version)
