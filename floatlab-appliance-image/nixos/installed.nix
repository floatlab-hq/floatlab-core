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
  # Mount persistent credentials in stage 1, before stage-2 user activation.
  fileSystems."/floatlab/system" = {
    device = "floatlab/system";
    fsType = "zfs";
    options = [ "zfsutil" ];
    neededForBoot = true;
  };
  fileSystems."/floatlab/system/etc" = {
    device = "floatlab/system/etc";
    fsType = "zfs";
    options = [ "zfsutil" ];
    neededForBoot = true;
  };
  users.mutableUsers = false;
  users.users.root.hashedPasswordFile = "/floatlab/system/etc/root-password.hash";
  services.timesyncd.enable = true;
  environment.systemPackages = [ pkgs.zfs pkgs.gitMinimal ];
  system.stateVersion = "26.05";
}
