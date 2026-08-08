#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
disk="${FLOATLAB_TEST_DISK:-$root/.test/floatlab-zfs.raw}"
size="${FLOATLAB_TEST_DISK_SIZE:-16G}"
mkdir -p "$(dirname "$disk")"

if [ ! -f "$disk" ]; then
  qemu-img create -f raw "$disk" "$size"
fi

iso="${1:-}"
if [ -z "$iso" ]; then
  iso="$(find -L "$root/result/iso" -maxdepth 1 -type f -name '*.iso' -print -quit 2>/dev/null || true)"
fi
if [ -z "$iso" ] || [ ! -f "$iso" ]; then
  echo "ISO not found. Run ./scripts/build-iso.sh first, or pass its path." >&2
  exit 1
fi

exec qemu-system-x86_64 \
  -enable-kvm \
  -machine q35,accel=kvm \
  -cpu host \
  -m 4096 \
  -smp 4 \
  -boot d \
  -cdrom "$iso" \
  -drive "file=$disk,format=raw,if=virtio,cache=none" \
  -nic user,model=virtio-net-pci \
  -serial mon:stdio \
  -display none
