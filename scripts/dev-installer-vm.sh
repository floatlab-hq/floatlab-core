#!/bin/sh
set -eu

repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
container=floatlab-installer-vm
if [ "${1:-}" = "--stop" ]; then
  docker stop "$container"
  exit
fi
mkdir -p "$repo_dir/.local/installer/vm/bin"
docker build -q -t floatlab-installer-preview-nix \
  -f "$repo_dir/floatlab-appliance-image/preview.Dockerfile" "$repo_dir/floatlab-appliance-image"
if ! docker container inspect "$container" >/dev/null 2>&1; then
  docker run -d --name "$container" \
    --mount type=volume,source=floatlab-installer-nix,target=/nix \
    --mount "type=bind,source=$repo_dir,target=/workspace" \
    --publish 127.0.0.1:6080:6080 \
    --env 'NIX_CONFIG=experimental-features = nix-command flakes
sandbox = false
extra-platforms = x86_64-linux
max-jobs = 2
cores = 2
filter-syscalls = false' \
    floatlab-installer-preview-nix sleep infinity
else
  docker start "$container" >/dev/null
fi

docker run --rm \
  --mount "type=bind,source=$repo_dir,target=/workspace" \
  --workdir /workspace \
  --env GOMODCACHE=/workspace/.local/installer/go-mod \
  --env GOCACHE=/workspace/.local/installer/go-build \
  --env GOARCH=amd64 --env CGO_ENABLED=0 \
  golang:1.26.3-bookworm \
  go build -o .local/installer/vm/bin/ ./cmd/install-tui ./cmd/install-service-mock

docker exec "$container" nix build --impure \
  --file /workspace/floatlab-appliance-image/preview-build.nix \
  --out-link /workspace/.local/installer/vm/runner

docker exec "$container" /workspace/.local/installer/vm/runner/bin/python3 \
  /workspace/scripts/installer-vm-control.py refresh || \
  docker exec -d "$container" sh -c \
    'exec /workspace/.local/installer/vm/runner/bin/floatlab-installer-preview > /workspace/.local/installer/vm/preview.log 2>&1'

echo 'Installer VM: http://127.0.0.1:6080/vnc.html?autoconnect=1&resize=scale'
