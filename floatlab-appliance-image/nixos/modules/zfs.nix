{ pkgs, ... }:
let
  importScript = pkgs.writeShellApplication {
    name = "floatlab-import-pool";
    runtimeInputs = [ pkgs.zfs pkgs.util-linux pkgs.coreutils ];
    text = ''
      set -euo pipefail

      if zpool list -H -o name floatlab >/dev/null 2>&1; then
        echo "floatlab pool is already imported"
        exit 0
      fi

      if ! zpool import -H -o name | grep -Fxq floatlab; then
        echo "FloatLab pool not found" >&2
        mkdir -p /run/floatlab
        printf '%s\n' pool-missing > /run/floatlab/boot-mode
        exit 1
      fi

      # Do not force-import automatically. A pool that appears active on
      # another host must enter maintenance rather than risk split-brain writes.
      zpool import -N -o cachefile=none floatlab
      zfs mount -a
      printf '%s\n' pool-imported > /run/floatlab/boot-mode
    '';
  };
in {
  boot.supportedFilesystems = [ "zfs" ];
  boot.kernelModules = [ "zfs" ];
  boot.zfs.forceImportRoot = false;
  environment.systemPackages = [ pkgs.zfs ];

  # Required by NixOS/OpenZFS. Replace with a stable per-node 8-hex-digit ID
  # once host identity persistence is implemented.
  networking.hostId = "f10a7ab0";

  systemd.services.floatlab-zfs-import = {
    description = "Discover and import the FloatLab ZFS pool";
    wantedBy = [ "multi-user.target" ];
    before = [ "floatlab-datasets.service" "docker.service" ];
    after = [ "systemd-udev-settle.service" ];
    wants = [ "systemd-udev-settle.service" ];
    unitConfig.DefaultDependencies = false;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${importScript}/bin/floatlab-import-pool";
      TimeoutStartSec = "90s";
    };
  };
}
