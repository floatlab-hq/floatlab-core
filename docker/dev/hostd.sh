#!/usr/bin/env bash
set -euo pipefail
cd /workspace
[ -f /floatlab/system/etc/development ] || { echo 'Enable dev mode on the host first.' >&2; exit 1; }
target=/floatlab/system/dev/bin
mkdir -p "$target"
temporary=$(mktemp "$target/.hostd.XXXXXX")
trap 'rm -f "$temporary"' EXIT
CGO_ENABLED=0 GOOS=linux go build -o "$temporary" ./cmd/floatlab-hostd
chmod 0755 "$temporary"
# Rename only a successful build; systemd watches this path and restarts hostd.
mv -f "$temporary" "$target/floatlab-hostd"
echo 'Installed hostd; check journalctl -u floatlab-hostd on the host.'
