#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
export VM_NAME="${VM_NAME:-floatlab-dev}"
export FLOATLAB_ANSWERS="${FLOATLAB_ANSWERS:-${1:-}}"
if [[ ! -f "$FLOATLAB_ANSWERS" ]]; then
  echo "Usage: $0 /path/to/answers.toml" >&2
  echo "Export answers with Ctrl+E on the installer Review screen; use FLOATLAB_INTEGRATION=1 go test -count=1 -timeout 12h -v ./integration for disposable E2E hosts." >&2
  exit 1
fi
iso="${FLOATLAB_APPLIANCE_ISO:-}"
if [[ -z "$iso" ]]; then
  (cd "$root/floatlab-appliance-image" && ./scripts/build-iso.sh iso)
  iso=$(find -L "$root/floatlab-appliance-image/result/iso" -maxdepth 1 -name '*.iso' -print -quit)
fi
exec "$root/floatlab-appliance-image/scripts/run-libvirt.sh" "$iso"
