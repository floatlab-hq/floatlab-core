#!/usr/bin/env bash
set -euo pipefail
tag=${1:-}
if [[ ! "$tag" =~ ^(host|manage|iso)-v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-([0-9A-Za-z-]+\.)*[0-9A-Za-z-]+)?$ ]]; then
  echo "Expected host-v1.0.0, manage-v1.0.0 or iso-v1.0.0 (optional prerelease): $tag" >&2
  exit 1
fi
version=${tag#*-v}
if [[ "$version" == *-* ]]; then
  IFS=. read -ra identifiers <<< "${version#*-}"
  for identifier in "${identifiers[@]}"; do
    if [[ "$identifier" =~ ^0[0-9]+$ ]]; then
      echo "Numeric prerelease identifiers must not have leading zeros" >&2
      exit 1
    fi
  done
fi
