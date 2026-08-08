{ pkgs, ... }:
{
  networking.hostName = "floatlab";
  time.timeZone = "Australia/Melbourne";

  boot.kernelParams = [
    "console=ttyS0,115200n8"
    "systemd.show_status=true"
    "rd.systemd.show_status=true"
  ];

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
    vim
  ];

  systemd.tmpfiles.rules = [
    "d /floatlab 0755 root root -"
    "d /run/floatlab 0755 root root -"
    "d /run/systemd/network 0755 root root -"
  ];

  # Keep the journal ephemeral and forward system, kernel and container logs to
  # the local VictoriaLogs syslog listener exposed by the management stack.
  services.journald.extraConfig = ''
    Storage=volatile
    RuntimeMaxUse=256M
    ForwardToSyslog=yes
  '';

  services.rsyslogd = {
    enable = true;
    extraConfig = ''
      *.* @@127.0.0.1:29514;RSYSLOG_SyslogProtocol23Format
    '';
  };
}
