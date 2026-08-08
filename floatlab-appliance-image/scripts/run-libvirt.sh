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

virsh -c "$uri" destroy "$name" >/dev/null 2>&1 || true
virsh -c "$uri" undefine "$name" --nvram >/dev/null 2>&1 || \
  virsh -c "$uri" undefine "$name" >/dev/null 2>&1 || true
virsh -c "$uri" vol-delete --pool "$pool" "$volume" >/dev/null 2>&1 || true
virsh -c "$uri" vol-create-as "$pool" "$volume" "${ZFS_DISK_SIZE:-16G}" \
  --format qcow2 --allocation 0 >/dev/null

virt-install --connect "$uri" \
  --name "$name" \
  --memory "${MEMORY_MB:-4096}" \
  --vcpus "${VCPUS:-4}" \
  --cpu host-passthrough \
  --import \
  --transient \
  --wait 0 \
  --osinfo detect=on,require=off \
  --boot cdrom \
  --disk "path=$iso,device=cdrom,readonly=on" \
  --disk "vol=$pool/$volume,bus=virtio,serial=floatlab-zfs" \
  --network network=default,model=virtio \
  --graphics none \
  --console pty,target_type=serial \
  --noautoconsole

echo "Started $name with a fresh ${ZFS_DISK_SIZE:-16G} floatlab pool disk."
echo "Console: virsh -c $uri console $name"
