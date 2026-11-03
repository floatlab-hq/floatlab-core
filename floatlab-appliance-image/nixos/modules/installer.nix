{ config, lib, pkgs, floatlab-installer-binaries, integrationCoverage ? false, ... }:
let
  root = "/run/floatlab-installer/root";
  chroot = "${pkgs.coreutils}/bin/chroot ${root} /run/current-system/sw/bin";
  rootSetup = pkgs.writeShellScript "floatlab-installer-root" (builtins.readFile ./installer-root.sh);
  answers = pkgs.writeShellApplication {
    name = "floatlab-find-install-answers";
    runtimeInputs = [ pkgs.coreutils pkgs.util-linux ];
    text = builtins.readFile ./installer-answers.sh;
  };
  gate = pkgs.writeShellScript "floatlab-installed-gate" ''
    test -e /floatlab/system/etc/installed || test -e /run/floatlab-installer/finalizing ||
      { test -e /floatlab/system/docker-compose.yml && ! test -e /floatlab/system/etc/install-operation.json; }
  '';
  identity = pkgs.writeShellApplication {
    name = "floatlab-restore-identity";
    runtimeInputs = [ pkgs.coreutils pkgs.jq pkgs.systemd ];
    text = ''
      if [ -f /floatlab/system/etc/appliance.json ]; then
        hostnamectl --transient set-hostname "$(jq -er .node_name /floatlab/system/etc/appliance.json)"
        timedatectl set-timezone "$(jq -er .timezone /floatlab/system/etc/appliance.json)"
      fi
    '';
  };
  discovery = pkgs.writeShellApplication {
    name = "floatlab-install-discovery";
    runtimeInputs = [ pkgs.coreutils pkgs.systemd ];
    text = ''
      if ${gate}; then
        systemctl start --no-block floatlab-core-stack.service
      else
        install -d -m 0700 /run/floatlab-installer
        if [ -f /floatlab/system/etc/install-status.json ]; then
          install -m 0600 /floatlab/system/etc/install-status.json /run/floatlab-installer/status.json
        fi
        if ${answers}/bin/floatlab-find-install-answers /iso /run/floatlab-installer; then
          systemctl start --no-block floatlab-unattended-install.service
        else
          result=$?
          if [ "$result" = 1 ]; then
            systemctl start --no-block floatlab-installer-display.service floatlab-installer-console@ttyS0.service
          else
            exit "$result"
          fi
        fi
      fi
    '';
  };
in {
  boot.initrd.systemd.enable = lib.mkForce false;
  services.timesyncd.enable = true;
  environment.systemPackages = [ floatlab-installer-binaries pkgs.gptfdisk pkgs.dosfstools pkgs.gnutar pkgs.gzip pkgs.nix pkgs.floatlab-zbm-kcl ];
  systemd.services.floatlab-boot-host-id = {
    wantedBy = [ "multi-user.target" ];
    before = [ "floatlab-zfs-import.service" ];
    after = [ "systemd-udev-settle.service" ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; ExecStart = "${identity}/bin/floatlab-restore-identity"; };
  };
  systemd.services.floatlab-zfs-import = {
    after = [ "floatlab-boot-host-id.service" ];
    requires = [ "floatlab-boot-host-id.service" ];
  };
  systemd.services.floatlab-host-identity = {
    after = [ "floatlab-datasets.service" ];
    before = [ "floatlab-network-config.service" ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; ExecStart = "${identity}/bin/floatlab-restore-identity"; };
  };
  systemd.services.floatlab-network-config = {
    environment.SYSTEMD_OFFLINE = "0";
    wants = [ "floatlab-host-identity.service" ];
    after = [ "floatlab-host-identity.service" "floatlab-installer-root.service" ];
    wantedBy = lib.mkForce [];
    requires = [ "floatlab-installer-root.service" ];
    serviceConfig.ExecStart = lib.mkForce "${chroot}/floatlab-installer-hostd network init";
    serviceConfig.ExecCondition = gate;
  };
  systemd.services."floatlab-installer-console@" = {
    description = "FloatLab installer console on %I";
    conflicts = [ "getty@%i.service" "serial-getty@%i.service" ];
    after = [ "systemd-udev-settle.service" "getty@%i.service" "serial-getty@%i.service" ];
    path = with pkgs; [ coreutils util-linux dosfstools gnutar gzip nix floatlab-zbm-kcl zfs systemd inetutils iproute2 ];
    serviceConfig = {
      ExecStart = "${floatlab-installer-binaries}/bin/install-tui";
      StandardInput = "tty-force";
      StandardOutput = "tty";
      StandardError = "tty";
      TTYPath = "/dev/%I";
      TTYReset = true;
      TTYVHangup = true;
      KillSignal = "SIGHUP";
      TimeoutStopSec = "infinity";
      UMask = "0077";
      Restart = "on-failure";
      Environment = "TERM=xterm-256color";
      RestartForceExitStatus = "SIGHUP";
      RestartSec = 5;
    };
  };
  systemd.services.floatlab-installer-display.path = [ pkgs.floatlab-zbm-kcl ];
  # Answers media opts into the same installer with exact disk confirmation.
  systemd.services.floatlab-unattended-install = {
    description = "Install FloatLab from private answers media";
    after = [ "systemd-udev-settle.service" "floatlab-installer-network.service" ];
    path = with pkgs; [ coreutils util-linux dosfstools gnutar gzip nix floatlab-zbm-kcl zfs systemd inetutils iproute2 ];
    script = ''
      set -euo pipefail
      if ${gate}; then exit 0; fi
      ${floatlab-installer-binaries}/bin/install-service --answers /run/floatlab-installer/answers.toml --reboot
    '';
    serviceConfig = { Type = "oneshot"; UMask = "0077"; TimeoutStartSec = "infinity"; TimeoutStopSec = "infinity"; };
  };
  systemd.services.floatlab-install-discovery = {
    wantedBy = [ "multi-user.target" ];
    wants = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    serviceConfig = { Type = "oneshot"; ExecStart = "${discovery}/bin/floatlab-install-discovery"; };
  };
  # Initial DHCP does not depend on storage. Remove it before hostd's bridge owns
  # the adapters, so it cannot compete with the confirmed installed settings.
  systemd.services.floatlab-installer-network = {
    wantedBy = [ "multi-user.target" ];
    wants = [ "systemd-networkd.service" "floatlab-datasets.service" ];
    after = [ "systemd-networkd.service" "floatlab-datasets.service" ];
    before = [ "floatlab-install-discovery.service" ];
    path = [ pkgs.coreutils pkgs.systemd ];
    script = ''
      if ! ${gate}; then
        mkdir -p /run/systemd/network
        install -m 0644 ${pkgs.writeText "floatlab-installer.network" "[Match]\nType=ether\nName=en* eth*\n[Network]\nDHCP=ipv4\n[Link]\nRequiredForOnline=no\n"} /run/systemd/network/90-floatlab-installer.network
        networkctl reload
        networkctl reconfigure 'en*' 'eth*' || true
      fi
    '';
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
  };
  systemd.services.floatlab-network-config.preStart = ''
    rm -f /run/systemd/network/90-floatlab-installer.network
    networkctl reload
  '';

  # Keep kernel support on the live host; userspace comes from the installed root.
  boot.kernelModules = [ "bridge" "veth" "br_netfilter" "xt_nat" ];
  boot.kernel.sysctl."net.ipv4.conf.all.forwarding" = true;
  boot.kernel.sysctl."net.ipv4.conf.default.forwarding" = true;
  systemd.services.floatlab-installer-root = {
    description = "Temporary installed root for installer verification";
    requires = [ "floatlab-datasets.service" "sshd-keygen.service" ];
    after = [ "floatlab-datasets.service" "sshd-keygen.service" ];
    path = with pkgs; [ coreutils util-linux zfs openssh systemd ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${rootSetup} prepare";
      ExecStopPost = "${rootSetup} cleanup";
      TimeoutStopSec = "120s";
    };
  };
  systemd.services.docker = {
    description = "Installed Docker daemon for installer verification";
    requires = [ "floatlab-installer-root.service" ];
    after = [ "floatlab-installer-root.service" "floatlab-network-config.service" ];
    serviceConfig = {
      Type = "notify";
      NotifyAccess = "all";
      # Docker exec requires a real root switch, not only chroot(2).
      RootDirectory = root;
      PrivateMounts = true;
      MountAPIVFS = false;
      ExecStart = "/run/current-system/sw/bin/floatlab-installer-docker";
      Delegate = true;
      TasksMax = "infinity";
      KillMode = "control-group";
      TimeoutStartSec = "120s";
      TimeoutStopSec = "120s";
    };
  };
  systemd.services.floatlab-hostd = {
    environment = { SYSTEMD_OFFLINE = "0"; } // lib.optionalAttrs integrationCoverage {
      GOCOVERDIR = "/floatlab/system/integration-coverage";
    };
    requires = [ "floatlab-installer-root.service" "floatlab-network-config.service" ];
    after = [ "floatlab-installer-root.service" "floatlab-network-config.service" ];
    serviceConfig = {
      ExecStart = "${chroot}/floatlab-installer-hostd";
      RuntimeDirectory = "floatlab";
      RuntimeDirectoryPreserve = "restart";
      RuntimeDirectoryMode = "0750";
      EnvironmentFile = "-/floatlab/system/etc/hostd.env";
      Restart = "on-failure";
      RestartSec = 2;
    };
  };
  systemd.services.floatlab-core-stack = {
    environment = lib.optionalAttrs integrationCoverage {
      GOCOVERDIR = "/floatlab/system/integration-coverage";
    };
    requires = [ "floatlab-installer-root.service" "docker.service" "floatlab-hostd.service" "floatlab-network-config.service" ];
    after = [ "floatlab-installer-root.service" "docker.service" "floatlab-hostd.service" "floatlab-network-config.service" "network-online.target" ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${chroot}/floatlab-start-core-stack";
      # ExecStopPost also tears down a partially started stack after startup fails.
      ExecStopPost = "${chroot}/docker-compose -f /floatlab/system/docker-compose.yml down";
      TimeoutStartSec = "10min";
      TimeoutStopSec = "120s";
    };
  };
}
