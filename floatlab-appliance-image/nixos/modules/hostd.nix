{ config, floatlab-binaries, pkgs, ... }:
{
  environment.systemPackages = [ (pkgs.writeShellApplication {
    name = "floatlab-installer-hostd";
    runtimeInputs = config.systemd.services.floatlab-hostd.path;
    text = ''exec ${floatlab-binaries}/bin/floatlab-hostd'';
  }) ];
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
      RuntimeDirectoryPreserve = "restart";
      EnvironmentFile = "-/floatlab/system/etc/hostd.env";
      RuntimeDirectoryMode = "0750";
    };
  };
}
