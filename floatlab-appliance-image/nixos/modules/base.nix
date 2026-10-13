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

  environment.systemPackages = with pkgs; [
    bashInteractive
    coreutils
    curl
    dig
    docker-compose
    ethtool
    gitMinimal
    htop
    iproute2
    jq
    lsof
    pciutils
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
