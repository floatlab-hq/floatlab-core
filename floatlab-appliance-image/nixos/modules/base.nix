{ pkgs, ... }:
{
  networking.hostName = ""; # The installer restores the persisted transient hostname.
  time.timeZone = null; # UTC until the installer restores the persisted timezone.

  boot.kernelParams = [
    "console=ttyS0,115200n8"
    "systemd.show_status=true"
    "rd.systemd.show_status=true"
  ];

  services.qemuGuest.enable = true;
  services.openssh.enable = true;
  services.openssh.settings.PermitRootLogin = "prohibit-password";

  users.groups.floatlab-replica = { };
  users.users.floatlab-replica = {
    isSystemUser = true;
    group = "floatlab-replica";
    home = "/var/lib/floatlab-replica";
    createHome = true;
    shell = pkgs.bashInteractive;
    # Public-key-only access is confined by forced-command authorized keys.
    hashedPassword = "*";
  };
  security.sudo.extraRules = [{
    users = [ "floatlab-replica" ];
    commands = [{ command = "/floatlab/system/etc/lab/replica-receive-root.sh"; options = [ "NOPASSWD" ]; }];
  }];

  environment.systemPackages = with pkgs; [
    bashInteractive
    coreutils
    curl
    dig
    ethtool
    htop
    iproute2
    jq
    lsof
    pciutils
    sqlite
    tmux
    usbutils
    nano
  ];

  systemd.tmpfiles.rules = [
    "d /floatlab 0755 root root -"
    "d /run/floatlab 0755 root root -"
    "d /run/systemd/network 0755 root root -"
  ];

  # Hostd forwards the ephemeral journal to VictoriaLogs over HTTP.
  services.journald.extraConfig = ''
    Storage=volatile
    RuntimeMaxUse=256M
    ForwardToSyslog=no
  '';
}
