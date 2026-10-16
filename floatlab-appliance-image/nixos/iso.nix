{ lib, modulesPath, floatlab-version, floatlab-development ? false, ... }:
{
  imports = [
    (modulesPath + "/installer/cd-dvd/installation-cd-minimal.nix")
    ./modules/base.nix
    ./modules/image-trim.nix
    ./modules/zfs.nix
    ./modules/persistence.nix
    ./modules/networking.nix
    ./modules/dev-provision.nix
    ./modules/installer.nix
    ./modules/installer-display.nix
  ] ++ lib.optionals floatlab-development [
    ./modules/docker.nix
    ./modules/hostd.nix
    ./modules/core-stack.nix
  ];

  image.baseName = lib.mkForce "floatlab-installer-${floatlab-version}";
  isoImage.volumeID = lib.mkForce "FLOATLAB";

  # The standard NixOS live ISO already uses a compressed squashfs store and
  # an ephemeral writable root layer. We intentionally retain that mechanism.
  installer.cloneConfig = false;
  isoImage.forceTextMode = false;

  programs.git.enable = lib.mkForce false;
  system.disableInstallerTools = true;
  system.installer.channel.enable = false;
  # Fetch expressions on demand instead of bundling the entire Nixpkgs tree.
  nix.nixPath = lib.mkForce [ "nixpkgs=https://channels.nixos.org/nixos-26.05/nixexprs.tar.xz" ];

  system.stateVersion = "26.05";
}
