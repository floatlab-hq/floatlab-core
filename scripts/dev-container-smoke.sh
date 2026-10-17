#!/usr/bin/env bash
# Runs only disposable containers and volumes; no appliance mounts or published ports.
set -euo pipefail
name="floatlab-dev-smoke-$$"
cleanup() {
    docker rm -f "$name" "$name-db" >/dev/null 2>&1 || true
    docker volume rm "$name-workspace" "$name-home" >/dev/null 2>&1 || true
    docker network rm "$name" >/dev/null 2>&1 || true
}
trap cleanup EXIT
docker network create "$name" >/dev/null
docker volume create "$name-workspace" >/dev/null
docker volume create "$name-home" >/dev/null
docker run -d --log-driver=local --name "$name-db" --network "$name" rqlite/rqlite:8.26.0 \
    -node-id=smoke -http-addr=0.0.0.0:4001 -raft-addr=0.0.0.0:4002 >/dev/null
start_control() {
  docker run -d --log-driver=local --name "$name" --network "$name" \
    -v "$name-workspace:/workspace" -v "$name-home:/root" floatlab-dev:local \
    --rqlite-url="http://$name-db:4001" --raft-bind=0.0.0.0:7000 \
    --raft-advertise="$name:7000" --raft-data=/root/raft --raft-bootstrap \
    --jwt-secret=smoke-test-only >/dev/null
}
start_control

ready() {
    for _ in $(seq 1 180); do
        if docker exec "$name" curl -fsS http://localhost:8080/api/v1/health > /dev/null 2>&1; then return; fi
        if [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ]; then break; fi
        sleep 1
    done
    docker logs "$name" >&2
    return 1
}
ready
docker exec "$name" curl -fsS http://localhost:8080/api/v1/health | grep -q '"dev_mode":true'
docker exec "$name" curl -fsS http://localhost:8080/ | grep -q '@vite/client'
docker exec "$name" curl -fsS http://localhost:9090/login | grep -qi 'password'
docker exec "$name" bash -c 'for tool in go git gh codex claude code-server air docker; do command -v "$tool" || exit 1; done'
docker exec "$name" bash -c 'codex --version && claude --version'
docker exec "$name" bash -c 'mkdir -p /floatlab/system/etc; touch /floatlab/system/etc/development; floatlab-dev-hostd; /floatlab/system/dev/bin/floatlab-hostd --help' >/dev/null

pid() {
    docker exec "$name" bash -c 'for file in /proc/[0-9]*/exe; do if [ "$(readlink "$file")" = /tmp/floatlab-control ]; then echo "$file"; fi; done'
}
before=$(pid)
[ -n "$before" ]
docker exec "$name" bash -c 'printf "\n// Smoke test reload.\n" >> cmd/floatlab-control/main.go'
reloaded=false
for _ in $(seq 1 60); do
    after=$(pid)
    if [ -n "$after" ] && [ "$before" != "$after" ]; then reloaded=true; break; fi
    sleep 1
done
"$reloaded"
ready

# Home and source must survive container replacement without being reseeded.
docker exec "$name" touch /root/credential-persistence-check
docker rm -f "$name" >/dev/null
start_control
ready
docker exec "$name" test -f /root/credential-persistence-check
docker exec "$name" grep -q 'Smoke test reload' cmd/floatlab-control/main.go
echo 'Development container: frontend, API, editor, tools, Go reload and persistence passed.'
