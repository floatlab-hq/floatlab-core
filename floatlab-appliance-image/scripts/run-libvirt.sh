#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
name="${VM_NAME:-floatlab-dev}"
pool="${LIBVIRT_POOL:-default}"
volume="$name-zfs.qcow2"
uri="${LIBVIRT_URI:-qemu:///system}"
iso="${1:-}"

for command in virsh virt-install; do
  command -v "$command" >/dev/null || {
    echo "missing command: $command" >&2
    exit 1
  }
done

if [ -z "$iso" ]; then
  iso="$(find -L "$root/result/iso" -maxdepth 1 -type f -name '*.iso' -print -quit 2>/dev/null || true)"
fi
[ -f "$iso" ] || {
  echo "ISO not found. Run ./scripts/build-iso.sh first, or pass its path." >&2
  exit 1
}
iso="$(readlink -f "$iso")"

if virsh -c "$uri" dominfo "$name" >/dev/null 2>&1 || virsh -c "$uri" vol-info --pool "$pool" "$volume" >/dev/null 2>&1 || virsh -c "$uri" vol-info --pool "$pool" "$name-answers.iso" >/dev/null 2>&1; then
  echo "Refusing to replace existing VM or volume $name." >&2
  exit 1
fi
extra_disks=()
if [[ -n "${FLOATLAB_ANSWERS:-}" ]]; then
  command -v xorriso >/dev/null || { echo "Missing xorriso; enter the appliance development shell." >&2; exit 1; }
  umask 077
  answers_dir=$(mktemp -d)
  trap 'rm -rf "$answers_dir"; rm -f "$answers_dir.iso"' EXIT
  cp "$FLOATLAB_ANSWERS" "$answers_dir/floatlab-answers.toml"
  xorriso -as mkisofs -quiet -R -V FLOATLAB_ANSWERS -o "$answers_dir.iso" "$answers_dir"
  answers_volume="$name-answers.iso"
  virsh -c "$uri" vol-create-as "$pool" "$answers_volume" "$(stat -c %s "$answers_dir.iso")" --format raw >/dev/null
  virsh -c "$uri" vol-upload --pool "$pool" "$answers_volume" "$answers_dir.iso"
  extra_disks=(--disk "vol=$pool/$answers_volume,device=cdrom,readonly=on")
fi
virsh -c "$uri" vol-create-as "$pool" "$volume" "${ZFS_DISK_SIZE:-16G}" \
  --format qcow2 --allocation 0 >/dev/null

virt-install --connect "$uri" \
  --name "$name" \
  --memory "${MEMORY_MB:-4096}" \
  --vcpus "${VCPUS:-4}" \
  --cpu host-passthrough \
  --import \
  --wait 0 \
  --osinfo detect=on,require=off \
  --boot uefi,loader.secure=no \
  --disk "path=$iso,device=cdrom,readonly=on,boot_order=2" \
  --disk "vol=$pool/$volume,bus=virtio,serial=floatlab-zfs,boot_order=1" \
  "${extra_disks[@]}" \
  --network "network=${LIBVIRT_NETWORK:-default},model=virtio" \
  --channel unix,target_type=virtio,name=org.qemu.guest_agent.0 \
  --graphics none \
  --console pty,target_type=serial \
  --noautoconsole

echo "Started $name with a blank ${ZFS_DISK_SIZE:-16G} disk and the FloatLab installer."
echo "Console: virsh -c $uri console $name"
