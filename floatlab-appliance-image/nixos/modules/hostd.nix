{ config, floatlab-binaries, pkgs, ... }:
{
  # PID1 feeds supported watchdog hardware; the independent trial guard still owns readiness.
  systemd.settings.Manager = {
    RuntimeWatchdogSec = "30s";
    RebootWatchdogSec = "2min";
  };
  environment.systemPackages = [ (pkgs.writeShellApplication {
    name = "floatlab-installer-hostd";
    runtimeInputs = config.systemd.services.floatlab-hostd.path;
    text = ''exec ${floatlab-binaries}/bin/floatlab-hostd "$@"'';
  }) ];
  systemd.services.floatlab-hostd = {
    description = "FloatLab host daemon";
    path = with pkgs; [ coreutils docker inetutils iproute2 openssh zfs systemd ethtool util-linux gnutar gzip nix efibootmgr floatlab-zbm-kcl nftables docker-compose ];
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-network-config.service" "floatlab-datasets.service" ];
    after = [ "floatlab-network-config.service" "floatlab-datasets.service" ];
    before = [ "floatlab-core-stack.service" ];
    serviceConfig = {
      ExecStart = pkgs.writeShellScript "floatlab-hostd-launch" ''
        if [ -f /floatlab/system/etc/development ] && [ -x /floatlab/system/dev/bin/floatlab-hostd ]; then
          exec /floatlab/system/dev/bin/floatlab-hostd
        fi
        exec ${floatlab-binaries}/bin/floatlab-hostd
      '';
      Restart = "on-failure";
      RestartSec = 2;
      RuntimeDirectory = "floatlab";
      RuntimeDirectoryPreserve = "restart";
      EnvironmentFile = "-/floatlab/system/etc/hostd.env";
      RuntimeDirectoryMode = "0750";
    };
  };
  # Independent from hostd and its network, Docker, and persistent-mount dependencies.
  systemd.services.floatlab-boot-recovery = {
    description = "Recover an uncommitted FloatLab OS boot";
    wantedBy = [ "sysinit.target" ];
    unitConfig.DefaultDependencies = false;
    after = [ "local-fs.target" ];
    before = [ "sysinit.target" "floatlab-datasets.service" "floatlab-network-config.service" "docker.service" "floatlab-hostd.service" ];
    path = [ pkgs.systemd ];
    serviceConfig = {
      Type = "simple";
      ExecStart = "${floatlab-binaries}/bin/floatlab-hostd boot-recovery";
      RuntimeDirectory = "floatlab-boot-recovery";
      RuntimeDirectoryMode = "0700";
      RuntimeDirectoryPreserve = "yes";
      Restart = "on-failure";
      RestartSec = 1;
    };
  };
  systemd.paths.floatlab-hostd-dev = {
    description = "Watch for a successfully deployed development host daemon";
    # Dataset setup runs after basic.target; the default paths.target ordering cycles.
    unitConfig.DefaultDependencies = false;
    conflicts = [ "shutdown.target" ];
    before = [ "shutdown.target" ];
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    pathConfig.PathChanged = "/floatlab/system/dev/bin/floatlab-hostd";
  };
  systemd.services.floatlab-hostd-dev = {
    description = "Restart hostd after development binary replacement";
    unitConfig.ConditionPathExists = "/floatlab/system/etc/development";
    serviceConfig = {
      Type = "oneshot";
      ExecStartPre = "${pkgs.systemd}/bin/systemctl reset-failed floatlab-hostd.service";
      ExecStart = "${pkgs.systemd}/bin/systemctl restart floatlab-hostd.service";
    };
  };
}
