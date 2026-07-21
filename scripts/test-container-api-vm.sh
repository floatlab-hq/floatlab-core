#!/usr/bin/env bash
set -euo pipefail

VM_NAME="${VM_NAME:-floatlab-dev}"
VM_DIR="${VM_DIR:-$HOME/.local/share/libvirt/images/$VM_NAME}"
POOL_NAME="${POOL_NAME:-floatlab}"
USERNAME="${USERNAME:-ubuntu}"
LIBVIRT_URI="${LIBVIRT_URI:-}"
SSH_KEY="${SSH_KEY:-}"
API_URL="${API_URL:-}"
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BUILD_DIR="$(mktemp -d)"
VIRSH=(virsh)

cd "$ROOT_DIR"

[[ -n "$LIBVIRT_URI" ]] && VIRSH+=(--connect "$LIBVIRT_URI")

die() {
  echo "error: $*" >&2
  exit 1
}

[[ "$VM_NAME" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || die "invalid VM_NAME: $VM_NAME"
[[ "$(basename "$VM_DIR")" == "$VM_NAME" ]] || die "VM_DIR must end with VM_NAME"

for command in virsh ssh scp go python3 curl; do
  command -v "$command" >/dev/null || die "missing command: $command"
done

if [[ -z "$SSH_KEY" ]]; then
  for candidate in "$HOME/.ssh/id_ed25519" "$HOME/.ssh/id_rsa"; do
    if [[ -f "$candidate" ]]; then
      SSH_KEY="$candidate"
      break
    fi
  done
fi
[[ -f "$SSH_KEY" ]] || die "set SSH_KEY to the private key installed in the VM"

cleanup() {
  rm -rf "$BUILD_DIR"
  if [[ -n "${VM_IP:-}" ]]; then
    ssh "${SSH_OPTS[@]}" "$USERNAME@$VM_IP" 'bash -s' -- "${STACK_ID:-}" <<'REMOTE_CLEANUP' || true
stack_id="$1"
sudo systemctl stop floatlab-integration-control floatlab-integration-hostd 2>/dev/null || true
if [[ -n "$stack_id" ]]; then
  sudo docker ps -aq --filter "label=com.docker.compose.project=$stack_id" | xargs -r sudo docker rm -f
fi
sudo docker rm -f floatlab-integration-rqlite >/dev/null 2>&1 || true
sudo zfs destroy -r floatlab/api-integration 2>/dev/null || true
REMOTE_CLEANUP
  fi
  "${VIRSH[@]}" destroy "$VM_NAME" >/dev/null 2>&1 || true
  "${VIRSH[@]}" undefine "$VM_NAME" --nvram >/dev/null 2>&1 || "${VIRSH[@]}" undefine "$VM_NAME" >/dev/null 2>&1 || true
  rm -f "$VM_DIR/os.qcow2" "$VM_DIR/${POOL_NAME}.qcow2" "$VM_DIR/seed.iso" "$VM_DIR/user-data" "$VM_DIR/meta-data"
  rmdir "$VM_DIR" 2>/dev/null || true
  echo "Removed test VM: $VM_NAME"
}
trap cleanup EXIT

echo "Waiting for VM address and cloud-init..."
for _ in {1..150}; do
  VM_IP="$("${VIRSH[@]}" domifaddr "$VM_NAME" --source lease 2>/dev/null | awk '/ipv4/ { sub("/.*", "", $4); print $4; exit }' || true)"
  [[ -n "$VM_IP" ]] || VM_IP="$("${VIRSH[@]}" domifaddr "$VM_NAME" --source agent 2>/dev/null | awk '/ipv4/ && $1 != "lo" && $1 != "docker0" { sub("/.*", "", $4); print $4; exit }' || true)"
  [[ -n "$VM_IP" ]] && break
  sleep 2
done
[[ -n "${VM_IP:-}" ]] || die "could not find an IPv4 address for $VM_NAME"

SSH_OPTS=(-i "$SSH_KEY" -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
for _ in {1..150}; do
  ssh "${SSH_OPTS[@]}" "$USERNAME@$VM_IP" cloud-init status --wait >/dev/null 2>&1 && break
  sleep 2
done
ssh "${SSH_OPTS[@]}" "$USERNAME@$VM_IP" cloud-init status --wait >/dev/null

echo "Building and installing FloatLab..."
CGO_ENABLED=0 GOCACHE="${GOCACHE:-/tmp/floatlab-go-cache}" go build -o "$BUILD_DIR/floatlab-hostd" ./cmd/floatlab-hostd
CGO_ENABLED=0 GOCACHE="${GOCACHE:-/tmp/floatlab-go-cache}" go build -o "$BUILD_DIR/floatlab-control" ./cmd/floatlab-control
scp "${SSH_OPTS[@]}" "$BUILD_DIR/floatlab-hostd" "$BUILD_DIR/floatlab-control" "$USERNAME@$VM_IP:/tmp/"

ssh "${SSH_OPTS[@]}" "$USERNAME@$VM_IP" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo install -m 0755 /tmp/floatlab-hostd /tmp/floatlab-control /usr/local/bin/
sudo systemctl stop floatlab-system floatlab-hostd 2>/dev/null || true
sudo systemctl stop floatlab-integration-control floatlab-integration-hostd 2>/dev/null || true
sudo systemctl reset-failed floatlab-integration-control floatlab-integration-hostd 2>/dev/null || true
sudo docker rm -f floatlab-integration-rqlite >/dev/null 2>&1 || true
sudo zfs destroy -r floatlab/api-integration 2>/dev/null || true
sudo rm -rf /var/lib/floatlab-integration
sudo mkdir -p /var/lib/floatlab-integration /floatlab
sudo docker run -d --name floatlab-integration-rqlite --network host rqlite/rqlite:8.26.0 \
  -raft-addr=127.0.0.1:4002 -raft-adv-addr=127.0.0.1:4002 >/dev/null
for _ in {1..60}; do
  curl -fsS http://127.0.0.1:4001/readyz >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS http://127.0.0.1:4001/readyz >/dev/null
sudo systemd-run --collect --unit=floatlab-integration-hostd /usr/local/bin/floatlab-hostd >/dev/null
for _ in {1..30}; do
  sudo test -S /run/floatlab/hostd.sock && break
  sleep 1
done
sudo test -S /run/floatlab/hostd.sock
sudo systemd-run --collect --unit=floatlab-integration-control \
  /usr/local/bin/floatlab-control --listen=:8080 --rqlite-url=http://127.0.0.1:4001 \
  --raft-id=node1 --raft-bind=127.0.0.1:7000 --raft-advertise=127.0.0.1:7000 \
  --raft-data=/var/lib/floatlab-integration/raft --raft-bootstrap \
  --jwt-secret=floatlab-integration --host-node-id=node1 >/dev/null
REMOTE

API_URL="${API_URL:-http://$VM_IP:8080}"
for _ in {1..60}; do
  HEALTH="$(curl -fsS "$API_URL/api/v1/health" 2>/dev/null || true)"
  [[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("raft_state", ""))' <<<"$HEALTH" 2>/dev/null || true)" == "Leader" ]] && break
  sleep 1
done
[[ "${HEALTH:-}" == *'"raft_state":"Leader"'* ]] || die "control API did not become leader"

TOKEN="$(python3 - <<'PY'
import base64, hashlib, hmac, json
encode = lambda value: base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).rstrip(b"=")
unsigned = b".".join((encode({"alg": "HS256", "typ": "JWT"}), encode({"sub": "integration", "roles": ["admin"]})))
print((unsigned + b"." + base64.urlsafe_b64encode(hmac.new(b"floatlab-integration", unsigned, hashlib.sha256).digest()).rstrip(b"=")).decode())
PY
)"
AUTH=(-H "Authorization: Bearer $TOKEN")

echo "Registering VM node and starting test stack..."
curl -fsS -X POST "$API_URL/api/v1/nodes" -H 'Content-Type: application/json' \
  -d '{"id":"node1","name":"integration-vm"}' >/dev/null
START_RESPONSE="$(curl -fsS -X POST "$API_URL/api/v1/stacks/api-integration/start" \
  "${AUTH[@]}" -H 'Idempotency-Key: integration-start' -H 'Content-Type: application/yaml' \
  --data-binary $'services:\n  sleeper:\n    image: alpine:3.20\n    command: ["sh", "-c", "while :; do sleep 60; done"]\n')"
STACK_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["stack_id"])' <<<"$START_RESPONSE")"

for _ in {1..90}; do
  STATUS="$(curl -fsS "${AUTH[@]}" "$API_URL/api/v1/stacks/$STACK_ID/status")"
  STATE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])' <<<"$STATUS")"
  [[ "$STATE" == "RunningPrimary" ]] && break
  [[ "$STATE" == "Failed" ]] && die "stack failed to start: $STATUS"
  sleep 2
done
[[ "$STATE" == "RunningPrimary" ]] || die "stack did not start: $STATUS"
python3 -c 'import json,sys; value=json.load(sys.stdin); assert len(value["containers"]) == 1 and value["containers"][0]["state"] == "running", value' <<<"$STATUS"

echo "Purging test stack..."
DELETE_RESPONSE="$(curl -fsS -X DELETE "${AUTH[@]}" -H 'Idempotency-Key: integration-delete' "$API_URL/api/v1/stacks/$STACK_ID?purge=true")"
OPERATION_ID="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["operation_id"])' <<<"$DELETE_RESPONSE")"
for _ in {1..90}; do
  OPERATION="$(curl -fsS "${AUTH[@]}" "$API_URL/api/v1/operations/$OPERATION_ID")"
  OPERATION_STATE="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])' <<<"$OPERATION")"
  [[ "$OPERATION_STATE" == "succeeded" ]] && break
  [[ "$OPERATION_STATE" == "failed" ]] && die "delete failed: $OPERATION"
  sleep 2
done
[[ "$OPERATION_STATE" == "succeeded" ]] || die "delete did not complete: $OPERATION"
ssh "${SSH_OPTS[@]}" "$USERNAME@$VM_IP" '! sudo zfs list floatlab/api-integration >/dev/null 2>&1'

echo "Container management API integration test passed."
