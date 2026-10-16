#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mkdir -p "$repo_dir/.local/installer"

exec docker run --rm -it \
  --mount "type=bind,source=$repo_dir,target=/workspace" \
  --workdir /workspace \
  --env TERM="${TERM:-xterm-256color}" \
  --env COLORTERM=truecolor \
  --env GOMODCACHE=/workspace/.local/installer/go-mod \
  --env GOCACHE=/workspace/.local/installer/go-build \
  golang:1.26.3-bookworm \
  sh -ec '
    go build -o .local/installer/bin/ ./cmd/install-tui ./cmd/install-service-mock
    exec .local/installer/bin/install-tui --mock "$@"
  ' sh "$@"
