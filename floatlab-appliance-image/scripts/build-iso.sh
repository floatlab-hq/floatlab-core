#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
export FLOATLAB_INSTALLER_VERSION="$(git describe --tags --exact-match HEAD 2>/dev/null || git rev-parse --short HEAD)"
nix build ".#${1:-iso}" --impure --print-build-logs
iso="$(find -L result/iso -maxdepth 1 -type f -name '*.iso' -print -quit)"
readlink -f "$iso"
