{ pkgs, ... }:
let
  reconcile = pkgs.writeShellApplication {
    name = "floatlab-reconcile-datasets";
    runtimeInputs = [ pkgs.zfs pkgs.coreutils pkgs.util-linux pkgs.systemd ];
    text = ''
      set -euo pipefail

      ensure_dataset() {
        local dataset="$1"
        local mountpoint="$2"
        shift 2

        if ! zfs list -H -o name "$dataset" >/dev/null 2>&1; then
          zfs create -o mountpoint="$mountpoint" "$@" "$dataset"
        elif [ "$(zfs get -H -o value mountpoint "$dataset")" != "$mountpoint" ]; then
          zfs set mountpoint="$mountpoint" "$dataset"
        fi
        mkdir -p "$mountpoint"
        if [ "$(zfs get -H -o value mounted "$dataset")" != yes ]; then
          zfs mount "$dataset"
        fi
      }

      ensure_dataset floatlab/system /floatlab/system
      ensure_dataset floatlab/system/etc /floatlab/system/etc
      ensure_dataset floatlab/system/docker /floatlab/system/docker
      ensure_dataset floatlab/system/db /floatlab/system/db -o compression=zstd
      ensure_dataset floatlab/system/metrics /floatlab/system/metrics -o compression=zstd
      ensure_dataset floatlab/system/logs /floatlab/system/logs -o compression=zstd
      ensure_dataset floatlab/system/updates /floatlab/system/updates
      ensure_dataset floatlab/system/management /floatlab/system/management
      # Run before Docker so dataset adoption has no live database writers.
      for name in rqlite raft victoria-metrics victoria-logs; do
        old="floatlab/system/$name"
        new="floatlab/system/management/$name"
        if zfs list -H -o name "$old" >/dev/null 2>&1; then
          if systemctl is-active --quiet docker.service; then
            echo "Stop Docker before management dataset adoption" >&2; exit 1
          fi
          if zfs list -H -o name "$new" >/dev/null 2>&1; then
            echo "Both old and new management datasets exist: $name" >&2; exit 1
          fi
          zfs rename "$old" "$new"
        fi
        ensure_dataset "$new" "/floatlab/system/management/$name" -o compression=zstd
        # Old Compose containers can start safely during the one-time layout transition.
        if [ ! -L "/floatlab/system/$name" ]; then
          rmdir "/floatlab/system/$name" 2>/dev/null || { test ! -e "/floatlab/system/$name"; }
          ln -s "management/$name" "/floatlab/system/$name"
        fi
      done

      mkdir -p \
        /floatlab/system/etc/systemd/network \
        /floatlab/system/docker \
        /floatlab/system/db \
        /floatlab/system/metrics \
        /floatlab/system/logs \
        /floatlab/system/raft \
        /floatlab/system/rqlite \
        /floatlab/system/victoria-metrics \
        /floatlab/system/victoria-logs

    '';
  };
in {
  systemd.services.floatlab-datasets = {
    description = "Create and mount FloatLab system datasets";
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-zfs-import.service" ];
    after = [ "floatlab-zfs-import.service" ];
    before = [ "floatlab-network-config.service" "docker.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${reconcile}/bin/floatlab-reconcile-datasets";
    };
  };
}
