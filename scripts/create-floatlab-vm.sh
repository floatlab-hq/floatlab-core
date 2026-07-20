#!/usr/bin/env bash
set -euo pipefail

VM_NAME="${VM_NAME:-floatlab-dev}"
UBUNTU_RELEASE="${UBUNTU_RELEASE:-noble}"
IMAGE_URL="${IMAGE_URL:-https://cloud-images.ubuntu.com/${UBUNTU_RELEASE}/current/${UBUNTU_RELEASE}-server-cloudimg-amd64.img}"
VM_DIR="${VM_DIR:-$HOME/.local/share/libvirt/images/$VM_NAME}"
MEMORY_MB="${MEMORY_MB:-4096}"
VCPUS="${VCPUS:-2}"
OS_DISK_SIZE="${OS_DISK_SIZE:-20G}"
ZFS_DISK_SIZE="${ZFS_DISK_SIZE:-20G}"
POOL_NAME="${POOL_NAME:-floatlab}"
USERNAME="${USERNAME:-ubuntu}"
RECREATE="${RECREATE:-0}"
OS_VARIANT="${OS_VARIANT:-ubuntu24.04}"
LIBVIRT_URI="${LIBVIRT_URI:-}"
RUN_INTEGRATION_TESTS="${RUN_INTEGRATION_TESTS:-0}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

OS_DISK="$VM_DIR/os.qcow2"
ZFS_DISK="$VM_DIR/${POOL_NAME}.qcow2"
SEED_ISO="$VM_DIR/seed.iso"
USER_DATA="$VM_DIR/user-data"
META_DATA="$VM_DIR/meta-data"
CACHE_DIR="${CACHE_DIR:-$HOME/.cache/floatlab-vm}"
BASE_IMAGE="$CACHE_DIR/${UBUNTU_RELEASE}-server-cloudimg-amd64.img"
VIRSH=(virsh)
VIRT_INSTALL=(virt-install)

if [[ -n "$LIBVIRT_URI" ]]; then
  VIRSH+=(--connect "$LIBVIRT_URI")
  VIRT_INSTALL+=(--connect "$LIBVIRT_URI")
fi

die() {
  echo "error: $*" >&2
  exit 1
}

have() {
  command -v "$1" >/dev/null 2>&1
}

need_cmd() {
  have "$1" || MISSING+=("$1")
}

make_seed_iso() {
  if have cloud-localds; then
    cloud-localds "$SEED_ISO" "$USER_DATA" "$META_DATA"
  elif have genisoimage; then
    genisoimage -quiet -output "$SEED_ISO" -volid cidata -joliet -rock "$USER_DATA" "$META_DATA"
  elif have mkisofs; then
    mkisofs -quiet -output "$SEED_ISO" -volid cidata -joliet -rock "$USER_DATA" "$META_DATA"
  elif have xorriso; then
    xorriso -as mkisofs -quiet -output "$SEED_ISO" -volid cidata -joliet -rock "$USER_DATA" "$META_DATA"
  else
    die "need one of cloud-localds, genisoimage, mkisofs, or xorriso to create the cloud-init seed ISO"
  fi
}

download_base_image() {
  mkdir -p "$CACHE_DIR"
  if [[ -s "$BASE_IMAGE" ]]; then
    echo "Using cached Ubuntu image: $BASE_IMAGE"
    return
  fi

  echo "Downloading Ubuntu cloud image:"
  echo "  $IMAGE_URL"
  if have curl; then
    curl -fL "$IMAGE_URL" -o "$BASE_IMAGE"
  elif have wget; then
    wget -O "$BASE_IMAGE" "$IMAGE_URL"
  else
    die "need curl or wget to download $IMAGE_URL"
  fi
}

pick_ssh_key() {
  if [[ -n "${SSH_PUB_KEY:-}" ]]; then
    [[ -f "$SSH_PUB_KEY" ]] || die "SSH_PUB_KEY does not exist: $SSH_PUB_KEY"
    return
  fi

  if [[ -f "$HOME/.ssh/id_ed25519.pub" ]]; then
    SSH_PUB_KEY="$HOME/.ssh/id_ed25519.pub"
  elif [[ -f "$HOME/.ssh/id_rsa.pub" ]]; then
    SSH_PUB_KEY="$HOME/.ssh/id_rsa.pub"
  else
    die "no SSH public key found; create one with ssh-keygen or set SSH_PUB_KEY=/path/to/key.pub"
  fi
}

existing_vm_state() {
  "${VIRSH[@]}" dominfo "$VM_NAME" >/dev/null 2>&1
}

destroy_existing_vm() {
  if ! existing_vm_state; then
    return
  fi

  if [[ "$RECREATE" != "1" ]]; then
    die "VM '$VM_NAME' already exists. Set RECREATE=1 to destroy and rebuild it."
  fi

  echo "Destroying existing VM: $VM_NAME"
  "${VIRSH[@]}" destroy "$VM_NAME" >/dev/null 2>&1 || true
  "${VIRSH[@]}" undefine "$VM_NAME" --nvram >/dev/null 2>&1 ||
    "${VIRSH[@]}" undefine "$VM_NAME"
}

check_existing_files() {
  local existing=()

  for path in "$OS_DISK" "$ZFS_DISK" "$SEED_ISO"; do
    [[ -e "$path" ]] && existing+=("$path")
  done

  if [[ ${#existing[@]} -eq 0 || "$RECREATE" == "1" ]]; then
    return
  fi

  printf 'error: VM files already exist. Set RECREATE=1 to replace them:\n' >&2
  printf '  %s\n' "${existing[@]}" >&2
  exit 1
}

MISSING=()
need_cmd qemu-img
need_cmd virt-install
need_cmd virsh
if [[ ${#MISSING[@]} -gt 0 ]]; then
  die "missing commands: ${MISSING[*]}. On Ubuntu/Debian install: sudo apt install qemu-utils virtinst libvirt-daemon-system libvirt-clients"
fi

pick_ssh_key
destroy_existing_vm
download_base_image

mkdir -p "$VM_DIR"
check_existing_files
rm -f "$OS_DISK" "$ZFS_DISK" "$SEED_ISO" "$USER_DATA" "$META_DATA"

echo "Creating OS disk: $OS_DISK"
qemu-img convert -f qcow2 -O qcow2 "$BASE_IMAGE" "$OS_DISK"
qemu-img resize "$OS_DISK" "$OS_DISK_SIZE"

echo "Creating ZFS disk: $ZFS_DISK"
qemu-img create -f qcow2 "$ZFS_DISK" "$ZFS_DISK_SIZE" >/dev/null

cat >"$META_DATA" <<EOF
instance-id: ${VM_NAME}
local-hostname: ${VM_NAME}
EOF

SSH_KEY_CONTENT="$(<"$SSH_PUB_KEY")"
cat >"$USER_DATA" <<EOF
#cloud-config
users:
  - name: ${USERNAME}
    groups: [adm, sudo]
    shell: /bin/bash
    sudo: ALL=(ALL) NOPASSWD:ALL
    ssh_authorized_keys:
      - ${SSH_KEY_CONTENT}

package_update: true
packages:
  - docker.io
  - zfsutils-linux
  - qemu-guest-agent

write_files:
  - path: /usr/local/sbin/floatlab-init-zfs
    permissions: '0755'
    owner: root:root
    content: |
      #!/usr/bin/env bash
      set -euo pipefail
      pool="${POOL_NAME}"
      disk="/dev/disk/by-id/virtio-floatlab-zfs"

      if zpool list -H "\$pool" >/dev/null 2>&1; then
        exit 0
      fi

      for _ in {1..30}; do
        [[ -e "\$disk" ]] && break
        sleep 1
      done

      [[ -e "\$disk" ]] || {
        echo "ZFS disk not found: \$disk" >&2
        exit 1
      }

      zpool create -f -o ashift=12 "\$pool" "\$disk"
      zfs set compression=lz4 "\$pool"
      zfs set atime=off "\$pool"

runcmd:
  - systemctl enable --now docker
  - usermod -aG docker ${USERNAME}
  - systemctl enable --now qemu-guest-agent
  - /usr/local/sbin/floatlab-init-zfs
EOF

echo "Creating cloud-init seed ISO: $SEED_ISO"
make_seed_iso

echo "Creating VM: $VM_NAME"
"${VIRT_INSTALL[@]}" \
  --name "$VM_NAME" \
  --memory "$MEMORY_MB" \
  --vcpus "$VCPUS" \
  --cpu host-passthrough \
  --import \
  --os-variant "$OS_VARIANT" \
  --disk "path=$OS_DISK,format=qcow2,bus=virtio" \
  --disk "path=$ZFS_DISK,format=qcow2,bus=virtio,serial=floatlab-zfs" \
  --disk "path=$SEED_ISO,device=cdrom" \
  --network network=default,model=virtio \
  --channel unix,target_type=virtio,name=org.qemu.guest_agent.0 \
  --graphics none \
  --console pty,target_type=serial \
  --noautoconsole

echo
echo "VM created and booting."
echo "Wait for cloud-init to finish, then connect with the guest-agent IP:"
if [[ -n "$LIBVIRT_URI" ]]; then
  echo "  ssh ${USERNAME}@\$(virsh --connect ${LIBVIRT_URI} domifaddr ${VM_NAME} --source agent | awk '/ipv4/ { sub(\"/.*\", \"\", \$4); print \$4; exit }')"
  echo "Or use the libvirt DHCP lease:"
  echo "  ssh ${USERNAME}@\$(virsh --connect ${LIBVIRT_URI} domifaddr ${VM_NAME} --source lease | awk '/ipv4/ { sub(\"/.*\", \"\", \$4); print \$4; exit }')"
else
  echo "  ssh ${USERNAME}@\$(virsh domifaddr ${VM_NAME} --source agent | awk '/ipv4/ { sub(\"/.*\", \"\", \$4); print \$4; exit }')"
  echo "Or use the libvirt DHCP lease:"
  echo "  ssh ${USERNAME}@\$(virsh domifaddr ${VM_NAME} --source lease | awk '/ipv4/ { sub(\"/.*\", \"\", \$4); print \$4; exit }')"
fi
echo
echo "Inside the VM, verify with:"
echo "  docker version"
echo "  zpool status ${POOL_NAME}"

if [[ "$RUN_INTEGRATION_TESTS" == "1" ]]; then
  (cd "$SCRIPT_DIR/.." && FLOATLAB_VM_INTEGRATION=1 go test -count=1 -v ./integration)
else
  echo "  FLOATLAB_VM_INTEGRATION=1 go test -count=1 -v ./integration"
fi
