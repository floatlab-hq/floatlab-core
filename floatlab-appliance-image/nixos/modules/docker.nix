{ lib, ... }:
{
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
