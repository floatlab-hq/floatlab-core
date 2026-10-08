{ pkgs, ... }:
let
  testDisk = "/dev/disk/by-id/virtio-floatlab-zfs";

  provision = pkgs.writeShellApplication {
    name = "floatlab-dev-provision";
    runtimeInputs = [ pkgs.zfs pkgs.util-linux pkgs.coreutils ];
    text = ''
      set -euo pipefail
      if [ "$#" -ne 1 ]; then
        echo "usage: floatlab-dev-provision /dev/vdX" >&2
        exit 2
      fi
      disk="$1"
      if zpool list -H -o name floatlab >/dev/null 2>&1 || zpool import -H -o name | grep -Fxq floatlab; then
        echo "A floatlab pool already exists; refusing to create another" >&2
        exit 1
      fi
      wipefs -a "$disk"
      zpool create -f -o cachefile=none -O compression=zstd -O atime=off floatlab "$disk"
      echo "Created test pool. Reboot to exercise the normal import path."
    '';
  };

  autoProvision = pkgs.writeShellApplication {
    name = "floatlab-dev-auto-provision";
    runtimeInputs = [ pkgs.zfs pkgs.util-linux pkgs.gnugrep ];
    text = ''
      set -euo pipefail
      if zpool list -H -o name floatlab >/dev/null 2>&1 || zpool import -H -o name | grep -Fxq floatlab; then
        exit 0
      fi
      wipefs -a ${testDisk}
      zpool create -f -o cachefile=none -O compression=zstd -O atime=off floatlab ${testDisk}
    '';
  };

in {
  environment.systemPackages = [ provision ];

  systemd.services.floatlab-dev-auto-provision = {
    description = "Create a fresh FloatLab test pool";
    wantedBy = [ "multi-user.target" ];
    before = [ "floatlab-zfs-import.service" ];
    after = [ "systemd-udev-settle.service" ];
    wants = [ "systemd-udev-settle.service" ];
    unitConfig = {
      ConditionPathExists = testDisk;
      DefaultDependencies = false;
    };
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${autoProvision}/bin/floatlab-dev-auto-provision";
    };
  };

}
