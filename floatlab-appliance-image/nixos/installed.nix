{ config, lib, pkgs, ... }:
{
  imports = [
    ./modules/base.nix
    ./modules/image-trim.nix
    ./modules/zfs.nix
    ./modules/persistence.nix
    ./modules/networking.nix
    ./modules/docker.nix
    ./modules/hostd.nix
    ./modules/core-stack.nix
    ./modules/identity.nix
  ];

  boot.loader.grub.enable = false;
  # Retain the installer's generic storage drivers before the ZFS root is available.
  hardware.enableAllHardware = true;
  boot.initrd.systemd.enable = false;
  boot.initrd.postResumeCommands = lib.mkAfter ''
    if ! sh -c ${lib.escapeShellArg (builtins.readFile ./modules/os-live.sh)}; then
      echo "Cannot recreate immutable OS root" >&2
      fail
      exit 1
    fi
  '';
  fileSystems."/" = { device = "floatlab/os/live"; fsType = "zfs"; };
  services.timesyncd.enable = true;
  environment.systemPackages = [ pkgs.zfs ];
  system.stateVersion = "26.05";
}
