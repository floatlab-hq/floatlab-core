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
  services.openssh.hostKeys = [
    { path = "/floatlab/system/etc/ssh/ssh_host_rsa_key"; type = "rsa"; bits = 4096; }
    { path = "/floatlab/system/etc/ssh/ssh_host_ed25519_key"; type = "ed25519"; }
  ];
  environment.etc."ssh/ssh_host_ed25519_key.pub".source = "/floatlab/system/etc/ssh/ssh_host_ed25519_key.pub";
  systemd.services.sshd-keygen = {
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
  };
  systemd.services.floatlab-hostd = {
    requires = [ "sshd-keygen.service" ];
    after = [ "sshd-keygen.service" ];
  };
  services.timesyncd.enable = true;
  environment.systemPackages = [ pkgs.zfs pkgs.gitMinimal ];
  system.stateVersion = "26.05";
}
