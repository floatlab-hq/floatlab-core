#!/usr/bin/env bash
set -euo pipefail
umask 077

if [ ! -d /workspace/.git ]; then
    # Refuse to seed over an existing workspace, including one with an interrupted copy.
    [ -z "$(ls -A /workspace)" ] || { echo 'Workspace is not empty and has no .git; recover it before starting.' >&2; exit 1; }
    cp -a /opt/floatlab-source/. /workspace/
    git switch -c "codex/appliance-$(date -u +%Y%m%d-%H%M%S)"
fi
CI=true pnpm install --offline --frozen-lockfile
# Vite accepts IP addresses by default; also permit the appliance's configured name.
if [ -n "${FLOATLAB_APPLIANCE_CONFIG:-}" ]; then
    __VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=$(node -e 'const fs = require("node:fs"); console.log(JSON.parse(fs.readFileSync(process.env.FLOATLAB_APPLIANCE_CONFIG)).node_name)')
    export __VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS
fi
mkdir -p /root/.config/code-server
if [ ! -e /root/.config/code-server/config.yaml ]; then
    printf 'bind-addr: 0.0.0.0:9090\nauth: password\npassword: %s\ncert: false\n' "$(head -c 32 /dev/urandom | base64)" > /root/.config/code-server/config.yaml
fi

# Air runs a shell-escaped command so all installed appliance arguments survive reloads.
printf '#!/usr/bin/env bash\nexec /tmp/floatlab-control ' > /tmp/floatlab-control-run
printf '%q ' "$@" --listen=127.0.0.1:8081 --dev-mode >> /tmp/floatlab-control-run
printf '\n' >> /tmp/floatlab-control-run
chmod +x /tmp/floatlab-control-run

children=()
# shellcheck disable=SC2329 # Invoked by the EXIT trap.
cleanup() { kill "${children[@]}" 2>/dev/null || true; wait || true; }
trap cleanup EXIT
trap 'exit 0' TERM INT
air -c /workspace/docker/dev/air.toml & children+=("$!")
pnpm --dir ui dev --host 0.0.0.0 --port 8080 --strictPort & children+=("$!")
code-server /workspace & children+=("$!")
set +e
wait -n "${children[@]}"
status=$?
set -e
# Any unexpected child exit must restart the whole development environment.
[ "$status" -ne 0 ] || status=1
exit "$status"
