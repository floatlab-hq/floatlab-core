#!/usr/bin/env bash
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo 'Run floatlab-mode as root on the appliance.' >&2; exit 1; }
cd /floatlab/system
exec 9>/run/floatlab-mode.lock
flock -n 9 || { echo 'Another mode change is in progress.' >&2; exit 1; }
case "${1:-status}" in
    status)
        if [ -f etc/development ]; then echo dev; else echo production; fi
        exit 0
        ;;
    dev)
        docker image inspect floatlab-dev:local >/dev/null
        install -d -m 0700 dev dev/home dev/workspace dev/bin
        touch etc/development
        ;;
    production)
        rm -f etc/development
        ;;
    *) echo 'Usage: floatlab-mode [status|dev|production]' >&2; exit 2;;
esac
systemctl reset-failed floatlab-hostd
systemctl restart floatlab-hostd
floatlab-start-core-stack
echo 'Mode applied. Workspace and tool credentials are retained in /floatlab/system/dev.'
