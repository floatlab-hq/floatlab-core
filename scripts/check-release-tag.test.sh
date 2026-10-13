#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

for version in v1.0.0 v0.1.0-alpha.0 v0.1.0-alpha.1 v0.1.0-rc v0.1.0-rc.1; do
  bash scripts/check-release-tag.sh "$version"
done
for version in '' main vnext v1 v1.0 v01.0.0 v1.0.0- v1.0.0-alpha..1 v1.0.0/alpha; do
  if bash scripts/check-release-tag.sh "$version" >/dev/null 2>&1; then
    echo "Unexpectedly accepted tag: $version" >&2
    exit 1
  fi
done
echo "Release tag checks passed"
