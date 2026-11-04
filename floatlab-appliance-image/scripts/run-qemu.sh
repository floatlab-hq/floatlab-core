#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
disk="${FLOATLAB_TEST_DISK:-$root/.test/floatlab-zfs.raw}"
size="${FLOATLAB_TEST_DISK_SIZE:-16G}"
firmware="${FLOATLAB_OVMF:?Set FLOATLAB_OVMF to the OVMF firmware directory, or enter nix develop}"
mkdir -p "$(dirname "$disk")"
vars="$disk.OVMF_VARS.fd"
if [[ ! -f "$vars" ]]; then cp "$firmware/OVMF_VARS.fd" "$vars"; fi

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

extra_disks=()
if [[ -n "${FLOATLAB_ANSWERS:-}" ]]; then
  umask 077
  answers_dir=$(mktemp -d)
  trap 'rm -rf "$answers_dir"; rm -f "$answers_dir.iso"' EXIT
  cp "$FLOATLAB_ANSWERS" "$answers_dir/floatlab-answers.toml"
  xorriso -as mkisofs -quiet -R -V FLOATLAB_ANSWERS -o "$answers_dir.iso" "$answers_dir"
  extra_disks=(-drive "file=$answers_dir.iso,media=cdrom,readonly=on")
fi

qemu-system-x86_64 \
  -enable-kvm \
  -machine q35,accel=kvm \
  -cpu host \
  -m 4096 \
  -smp 4 \
  -drive "if=pflash,format=raw,readonly=on,file=$firmware/OVMF_CODE.fd" \
  -drive "if=pflash,format=raw,file=$vars" \
  -boot order=cd \
  -cdrom "$iso" \
  -drive "file=$disk,format=raw,if=virtio,cache=none,serial=floatlab-zfs" \
  "${extra_disks[@]}" \
  -nic user,model=virtio-net-pci \
  -serial mon:stdio \
  -display none
