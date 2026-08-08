{ config, lib, modulesPath, pkgs, ... }:
{
  imports = [
    (modulesPath + "/installer/cd-dvd/installation-cd-minimal.nix")
    ./modules/base.nix
    ./modules/zfs.nix
    ./modules/persistence.nix
    ./modules/networking.nix
    ./modules/docker.nix
    ./modules/hostd.nix
    ./modules/core-stack.nix
    ./modules/dev-provision.nix
  ];

  image.fileName = lib.mkForce "floatlab-appliance-${config.system.nixos.label}-${pkgs.stdenv.hostPlatform.system}.iso";
  isoImage.volumeID = lib.mkForce "FLOATLAB";

  # The standard NixOS live ISO already uses a compressed squashfs store and
  # an ephemeral writable root layer. We intentionally retain that mechanism.
  installer.cloneConfig = false;

  system.stateVersion = "26.05";
}
