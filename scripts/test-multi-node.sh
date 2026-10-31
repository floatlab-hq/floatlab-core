#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

for tool in go nix virsh virt-install python3; do
  command -v "$tool" >/dev/null || { echo "Missing $tool; enter the development shell." >&2; exit 1; }
done
[[ -e /dev/kvm ]] || { echo "Multi-node acceptance requires /dev/kvm." >&2; exit 1; }
[[ -z "${API_URL:-}${VM_NAME:-}${VM_SSH:-}" ]] || {
  echo "Multi-node acceptance creates its own appliances; unset API_URL, VM_NAME and VM_SSH." >&2
  exit 1
}
export LIBVIRT_URI="${LIBVIRT_URI:-qemu:///system}"
virsh -c "$LIBVIRT_URI" uri >/dev/null
volume_dir="$(mktemp -d /tmp/floatlab-multinode-volumes.XXXXXX)"
chmod 0755 "$volume_dir"
export LIBVIRT_POOL="floatlab-multinode-$(basename "$volume_dir")"
export FLOATLAB_MULTI_NODE_RUN_ID="${volume_dir##*.}"
virsh -c "$LIBVIRT_URI" pool-create-as "$LIBVIRT_POOL" dir --target "$volume_dir"
export LIBVIRT_NETWORK="floatlab-multinode-$FLOATLAB_MULTI_NODE_RUN_ID"
cleanup_pool() {
  if [[ "${FLOATLAB_KEEP_VM:-0}" == 1 ]]; then
    echo "Keeping test storage pool $LIBVIRT_POOL at $volume_dir."
  else
    # Also clean up after go test is killed before its t.Cleanup callbacks run.
    while IFS= read -r domain; do
      case "$domain" in
        "floatlab-multinode-$FLOATLAB_MULTI_NODE_RUN_ID-"*)
          virsh -c "$LIBVIRT_URI" destroy "$domain" >/dev/null 2>&1 || true
          virsh -c "$LIBVIRT_URI" undefine "$domain" --nvram >/dev/null 2>&1 || true
          ;;
      esac
    done < <(virsh -c "$LIBVIRT_URI" list --all --name)
    while IFS= read -r volume; do
      [[ -z "$volume" ]] || virsh -c "$LIBVIRT_URI" vol-delete --pool "$LIBVIRT_POOL" "$volume" || true
    done < <(virsh -c "$LIBVIRT_URI" vol-list --pool "$LIBVIRT_POOL" | awk 'NR > 2 && NF {print $1}')
    virsh -c "$LIBVIRT_URI" pool-destroy "$LIBVIRT_POOL" || true
    virsh -c "$LIBVIRT_URI" net-destroy "$LIBVIRT_NETWORK" >/dev/null 2>&1 || true
    rm -f "$volume_dir/network.xml"
    rmdir "$volume_dir" || true
  fi
}
trap cleanup_pool EXIT
# Avoid host routes and all configured libvirt subnets, including inactive ones.
python3 - "$volume_dir/network.xml" <<'PY'
import ipaddress, json, os, subprocess, sys, xml.etree.ElementTree as ET
uri = os.environ["LIBVIRT_URI"]
used = [ipaddress.ip_network(row["dst"], strict=False)
        for row in json.loads(subprocess.check_output(["ip", "-json", "route", "show", "table", "all"]))
        if row.get("dst", "default") != "default" and ":" not in row["dst"]]
names = subprocess.check_output(["virsh", "-c", uri, "net-list", "--all", "--name"], text=True).split()
for name in names:
    root = ET.fromstring(subprocess.check_output(["virsh", "-c", uri, "net-dumpxml", name]))
    for ip in root.findall("ip"):
        if ":" not in ip.get("address", ""):
            used.append(ipaddress.ip_network(ip.attrib["address"] + "/" + ip.get("prefix", ip.get("netmask", "24")), strict=False))
subnet = next((ipaddress.ip_network(f"198.19.{octet}.0/24") for octet in range(1, 255)
               if not any(ipaddress.ip_network(f"198.19.{octet}.0/24").overlaps(old) for old in used)), None)
if subnet is None:
    raise SystemExit("No unused integration subnet in 198.19.0.0/16")
name = os.environ["LIBVIRT_NETWORK"]
bridge = "flmn" + os.environ["FLOATLAB_MULTI_NODE_RUN_ID"]
with open(sys.argv[1], "w") as output:
    output.write(f"<network><name>{name}</name><forward mode='nat'/><bridge name='{bridge}'/><ip address='{subnet[1]}' netmask='255.255.255.0'><dhcp><range start='{subnet[2]}' end='{subnet[99]}'/></dhcp></ip></network>")
PY
virsh -c "$LIBVIRT_URI" net-create "$volume_dir/network.xml"
export MEMORY_MB="${MEMORY_MB:-4096}"
report_dir="${FLOATLAB_REPORT_DIR:-/tmp/floatlab-multi-node-reports}"
mkdir -p "$report_dir"
FLOATLAB_MULTI_NODE_INTEGRATION=1 go test -count=1 -timeout 150m \
  -run '^TestMultiNodeLab$' -v ./integration 2>&1 | tee "$report_dir/multi-node.log"
