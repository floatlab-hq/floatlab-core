{ pkgs, floatlab-installer-binaries, floatlab-installer-command, ... }:
let
  command = if floatlab-installer-command == null
    then "${floatlab-installer-binaries}/bin/install-tui"
    else floatlab-installer-command;
  plex = pkgs.ibm-plex.override { families = [ "mono" ]; };
  braille = pkgs.runCommand "floatlab-braille-font" { } ''
    install -Dm644 ${../../fonts/FloatLabBraille-Regular.ttf} $out/share/fonts/truetype/FloatLabBraille-Regular.ttf
  '';
  configuration = pkgs.writeTextFile {
    name = "floatlab-installer-kmscon";
    destination = "/kmscon.conf";
    text = ''
      font-engine=pango
      font-name=FloatLab Braille, IBM Plex Mono, Unifont
      font-size=16
      drm
      no-hwaccel
      xkb-layout=us
      xkb-model=pc105
      term=xterm-256color
    '';
  };
in {
  fonts = {
    packages = [ plex braille pkgs.unifont ];
    enableDefaultPackages = false;
    fontconfig = {
      enable = true;
      defaultFonts.monospace = [ "FloatLab Braille" "IBM Plex Mono" "Unifont" ];
    };
  };

  systemd.services.floatlab-installer-display = {
    description = "FloatLab installer display with IBM Plex Mono";
    conflicts = [ "getty@tty1.service" "kmsconvt@tty1.service" "floatlab-installer-console@tty1.service" ];
    wants = [ "systemd-logind.service" ];
    after = [ "systemd-udev-settle.service" "systemd-logind.service" "getty@tty1.service" ];
    environment = { TERM = "xterm-256color"; COLORTERM = "truecolor"; };
    path = with pkgs; [ coreutils util-linux dosfstools gnutar gzip nix zfs systemd inetutils iproute2 ];
    serviceConfig = {
      # kmscon 9 sets COLORTERM=kmscon for its child; restore RGB after that step.
      ExecStart = "${pkgs.kmscon}/bin/kmscon --configdir ${configuration} --vt=tty1 --no-switchvt --login -- ${pkgs.coreutils}/bin/env COLORTERM=truecolor TERM=xterm-256color ${command}";
      KillSignal = "SIGHUP";
      TimeoutStopSec = "infinity";
      UMask = "0077";
      Restart = "on-failure";
      RestartForceExitStatus = "SIGHUP";
      RestartSec = 2;
    };
  };
}
