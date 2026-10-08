{ floatlab-binaries, pkgs, ... }:
{
  systemd.services.floatlab-hostd = {
    description = "FloatLab host daemon";
    path = with pkgs; [ coreutils docker inetutils iproute2 openssh zfs systemd ethtool ];
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-network-config.service" "floatlab-datasets.service" ];
    after = [ "floatlab-network-config.service" "floatlab-datasets.service" ];
    before = [ "floatlab-core-stack.service" ];
    serviceConfig = {
      ExecStart = "${floatlab-binaries}/bin/floatlab-hostd";
      Restart = "on-failure";
      RestartSec = 2;
      RuntimeDirectory = "floatlab";
      RuntimeDirectoryMode = "0750";
    };
  };
}
