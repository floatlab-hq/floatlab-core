{ config, lib, pkgs, floatlab-binaries, floatlab-development ? false, ... }:
let
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
        systemctl start --no-block floatlab-installer-console@tty1.service floatlab-installer-console@ttyS0.service
      fi
    '';
  };
in {
  boot.initrd.systemd.enable = lib.mkForce false;
  services.timesyncd.enable = true;
  environment.systemPackages = [ floatlab-binaries pkgs.gptfdisk pkgs.dosfstools pkgs.gnutar pkgs.gzip pkgs.nix pkgs.floatlab-zbm-kcl ];
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
    wants = [ "floatlab-host-identity.service" ];
    after = [ "floatlab-host-identity.service" ];
    wantedBy = lib.mkIf (!floatlab-development) (lib.mkForce []);
    serviceConfig.ExecCondition = lib.mkIf (!floatlab-development) gate;
  };
  systemd.services."floatlab-installer-console@" = {
    description = "FloatLab installer console on %I";
    conflicts = [ "getty@%i.service" "serial-getty@%i.service" ];
    after = [ "systemd-udev-settle.service" "getty@%i.service" "serial-getty@%i.service" ];
    path = with pkgs; [ coreutils util-linux dosfstools gnutar gzip nix floatlab-zbm-kcl zfs systemd inetutils iproute2 ];
    serviceConfig = {
      ExecStart = "${floatlab-binaries}/bin/install-tui";
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
  systemd.services.floatlab-install-discovery = lib.mkIf (!floatlab-development) {
    wantedBy = [ "multi-user.target" ];
    wants = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    serviceConfig = { Type = "oneshot"; ExecStart = "${discovery}/bin/floatlab-install-discovery"; };
  };
  # Initial DHCP does not depend on storage. Remove it before hostd's bridge owns
  # the adapters, so it cannot compete with the confirmed installed settings.
  systemd.services.floatlab-installer-network = lib.mkIf (!floatlab-development) {
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
  systemd.services.floatlab-network-config.preStart = lib.mkIf (!floatlab-development) ''
    rm -f /run/systemd/network/90-floatlab-installer.network
    networkctl reload
  '';

  systemd.services.docker = lib.mkIf (!floatlab-development) { wantedBy = lib.mkForce []; serviceConfig.ExecCondition = gate; };
  systemd.services.floatlab-hostd = lib.mkIf (!floatlab-development) { wantedBy = lib.mkForce []; serviceConfig.ExecCondition = gate; };

  systemd.services.floatlab-core-stack = lib.mkIf (!floatlab-development) { wantedBy = lib.mkForce []; serviceConfig.ExecCondition = gate; };
  systemd.sockets.docker.wantedBy = lib.mkIf (!floatlab-development) (lib.mkForce []);
}
