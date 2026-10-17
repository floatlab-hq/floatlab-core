#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export FLOATLAB_INSTALLER_VERSION="${FLOATLAB_INSTALLER_VERSION:-$(git describe --tags --exact-match --match 'iso-v*' HEAD 2>/dev/null || git rev-parse --short HEAD)}"
if [ -z "${FLOATLAB_ISO_BUNDLE:-}" ]; then
  bundle=$(mktemp -d)
  trap 'rm -rf "$bundle"' EXIT
  nix build .#host-image --impure --out-link "$bundle/host" --print-build-logs
  python3 ../scripts/release.py prepare-iso "$bundle/bundle" --version "$FLOATLAB_INSTALLER_VERSION" --local-host "$bundle/host"
  export FLOATLAB_ISO_BUNDLE="$bundle/bundle"
fi
revision=$(python3 -c 'import json,os; print(json.load(open(os.environ["FLOATLAB_ISO_BUNDLE"]+"/floatlab-manifest.json"))["nixpkgs_rev"])')
nix build ".#${1:-iso}" --impure --override-input nixpkgs "github:NixOS/nixpkgs/$revision" --no-write-lock-file --print-build-logs
iso="$(find -L result/iso -maxdepth 1 -type f -name '*.iso' -print -quit)"
readlink -f "$iso"
