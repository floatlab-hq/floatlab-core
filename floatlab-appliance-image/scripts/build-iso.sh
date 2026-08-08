#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
nix build .#iso --print-build-logs
iso="$(find -L result/iso -maxdepth 1 -type f -name '*.iso' -print -quit)"
readlink -f "$iso"
