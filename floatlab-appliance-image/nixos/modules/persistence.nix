{ pkgs, ... }:
let
  reconcile = pkgs.writeShellApplication {
    name = "floatlab-reconcile-datasets";
    runtimeInputs = [ pkgs.zfs pkgs.coreutils pkgs.util-linux ];
    text = ''
      set -euo pipefail

      ensure_dataset() {
        local dataset="$1"
        local mountpoint="$2"
        shift 2

        if ! zfs list -H -o name "$dataset" >/dev/null 2>&1; then
          zfs create -o mountpoint="$mountpoint" "$@" "$dataset"
        else
          zfs set mountpoint="$mountpoint" "$dataset"
        fi
        mkdir -p "$mountpoint"
        zfs mount "$dataset" 2>/dev/null || true
      }

      ensure_dataset floatlab/system /floatlab/system
      ensure_dataset floatlab/system/etc /floatlab/system/etc
      ensure_dataset floatlab/system/docker /floatlab/system/docker
      ensure_dataset floatlab/system/db /floatlab/system/db -o compression=zstd
      ensure_dataset floatlab/system/metrics /floatlab/system/metrics -o compression=zstd
      ensure_dataset floatlab/system/logs /floatlab/system/logs -o compression=zstd
      ensure_dataset floatlab/system/raft /floatlab/system/raft -o compression=zstd
      ensure_dataset floatlab/system/rqlite /floatlab/system/rqlite -o compression=zstd
      ensure_dataset floatlab/system/victoria-metrics /floatlab/system/victoria-metrics -o compression=zstd
      ensure_dataset floatlab/system/victoria-logs /floatlab/system/victoria-logs -o compression=zstd

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

      # Persist networkd configuration without attempting to mutate NixOS /etc.
      # /run is the supported runtime configuration tier for systemd-networkd.
      mountpoint -q /run/systemd/network || \
        mount --bind /floatlab/system/etc/systemd/network /run/systemd/network
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
