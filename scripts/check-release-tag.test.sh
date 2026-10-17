#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
for component in host manage iso; do
  for version in 1.0.0 0.1.0-alpha.0 0.1.0-alpha.1 0.1.0-rc 0.1.0-rc.1; do
    bash scripts/check-release-tag.sh "$component-v$version"
  done
done
for version in '' main vnext v1.0.0 host-v1 host-v1.0 manage-v01.0.0 iso-v1.0.0- manage-v1.0.0-alpha..1 host-v1.0.0/alpha manage-v1.0.0-alpha.01 cli-v1.0.0; do
  if bash scripts/check-release-tag.sh "$version" >/dev/null 2>&1; then
    echo "Unexpectedly accepted tag: $version" >&2
    exit 1
  fi
done
echo "Release tag checks passed"
