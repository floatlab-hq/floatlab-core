#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
for tool in go bun pnpm; do
  command -v "$tool" >/dev/null || { echo "Missing $tool; enter the development shell and install workspace dependencies." >&2; exit 1; }
done
if [[ -z "${API_URL:-}" ]]; then
  FLOATLAB_RQLITE_INTEGRATION=1 go test -count=1 -timeout 5m \
    -run '^TestPinnedRQLiteMigrationAndLocalAuthority$' -v ./cmd/floatlab-control
  FLOATLAB_DOCKER_INTEGRATION=1 go test -count=1 -timeout 5m \
    -run '^TestComposeCachedStartAndExplicitPull$' -v ./pkg/docker
fi
if [[ -n "${API_URL:-}" ]]; then
  if [[ -n "${VM_SSH:-}" ]]; then
    command -v ssh >/dev/null || { echo "Missing ssh." >&2; exit 1; }
  elif [[ -n "${VM_NAME:-}" ]]; then
    command -v virsh >/dev/null || { echo "Missing virsh." >&2; exit 1; }
    virsh -c "${LIBVIRT_URI:-qemu:///system}" domstate "$VM_NAME" >/dev/null
  else
    echo "API_URL requires VM_SSH or VM_NAME pointing to a dedicated test appliance." >&2
    exit 1
  fi
else
  single_memory_mb="${MEMORY_MB:-8192}"
  if ! [[ "$single_memory_mb" =~ ^[0-9]+$ ]] || (( single_memory_mb < 8192 )); then
    echo "The full workload suite needs MEMORY_MB >= 8192 for the management-update sandbox and host headroom." >&2
    exit 1
  fi
  [[ -e /dev/kvm ]] || { echo "Missing /dev/kvm; use a KVM host or API_URL and VM_SSH." >&2; exit 1; }
  for tool in nix virsh virt-install; do
    command -v "$tool" >/dev/null || { echo "Missing $tool." >&2; exit 1; }
  done
  virsh -c "${LIBVIRT_URI:-qemu:///system}" uri >/dev/null
  if [[ -z "${LIBVIRT_POOL:-}" ]]; then
    volume_dir="$(mktemp -d /tmp/floatlab-confidence-volumes.XXXXXX)"
    chmod 0755 "$volume_dir"
    export LIBVIRT_POOL="floatlab-confidence-$(basename "$volume_dir")"
    virsh -c "${LIBVIRT_URI:-qemu:///system}" pool-create-as "$LIBVIRT_POOL" dir --target "$volume_dir"
    cleanup_pool() {
      if [[ "${FLOATLAB_KEEP_VM:-0}" == 1 ]]; then
        echo "Keeping test storage pool $LIBVIRT_POOL at $volume_dir."
      else
        virsh -c "${LIBVIRT_URI:-qemu:///system}" pool-destroy "$LIBVIRT_POOL" || true
        rmdir "$volume_dir" || true
      fi
    }
    trap cleanup_pool EXIT
  else
    virsh -c "${LIBVIRT_URI:-qemu:///system}" pool-info "$LIBVIRT_POOL" >/dev/null
  fi
fi
report_dir="${FLOATLAB_REPORT_DIR:-/tmp/floatlab-workload-reports}"
mkdir -p "$report_dir"
for pass in 1 2; do
  MEMORY_MB="${single_memory_mb:-${MEMORY_MB:-8192}}" FLOATLAB_WORKLOAD_INTEGRATION=1 FLOATLAB_COVERAGE_REPORT="$report_dir/pass-$pass.json" \
    go test -count=1 -timeout 90m -run '^TestWorkloadConfidence$' -v ./integration 2>&1 | tee "$report_dir/pass-$pass.log"
done
if [[ -z "${API_URL:-}" ]]; then
  scripts/test-multi-node.sh
else
  echo "Existing-appliance mode covers one node; run scripts/test-multi-node.sh on a KVM host for lab acceptance."
fi
