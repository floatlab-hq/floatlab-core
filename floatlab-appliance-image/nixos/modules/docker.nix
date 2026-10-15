{ config, pkgs, ... }:
{
  environment.systemPackages = [ pkgs.docker-compose (pkgs.writeShellApplication {
    name = "floatlab-installer-docker";
    runtimeInputs = [ pkgs.docker pkgs.kmod config.boot.zfs.package ];
    text = ''
      exec dockerd --config-file=${pkgs.writeText "floatlab-installer-docker.json" (builtins.toJSON
        (config.virtualisation.docker.daemon.settings // {
          hosts = [ "unix:///run/docker.sock" ];
          group = "root";
          "live-restore" = false;
        }))}
    '';
  }) ];
  virtualisation.docker = {
    enable = true;
    enableOnBoot = true;
    storageDriver = "zfs";
    daemon.settings = {
      "data-root" = "/floatlab/system/docker";
      "log-driver" = "journald";
      "live-restore" = true;
      "default-address-pools" = [
        { base = "172.28.0.0/16"; size = 24; }
      ];
    };
  };

  systemd.services.docker = {
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    unitConfig.ConditionPathIsMountPoint = "/floatlab/system";
  };

  # Docker Compose is invoked explicitly by floatlab-core-stack.service.
  virtualisation.oci-containers.backend = "docker";
}
