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

  seedNetwork = pkgs.writeShellApplication {
    name = "floatlab-dev-seed-network";
    runtimeInputs = [ pkgs.coreutils pkgs.findutils pkgs.gnugrep ];
    text = ''
      set -euo pipefail
      config_dir=/floatlab/system/etc/systemd/network
      if find "$config_dir" -maxdepth 1 -type f -name '*.network' -print -quit | grep -q .; then
        exit 0
      fi
      install -Dm0644 ${pkgs.writeText "floatlab-dev-dhcp.network" ''
        [Match]
        Name=en* eth*

        [Network]
        DHCP=yes
      ''} "$config_dir/20-dhcp.network"
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

  systemd.services.floatlab-dev-seed-network = {
    description = "Seed DHCP networking for the FloatLab test VM";
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    before = [ "floatlab-network-config.service" ];
    unitConfig.ConditionPathExists = testDisk;
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${seedNetwork}/bin/floatlab-dev-seed-network";
    };
  };
}
