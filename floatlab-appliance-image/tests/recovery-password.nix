{ pkgs, installed }:
{ lib, ... }: {
  virtualisation.qemu.networkingOptions = lib.mkForce [ "-nic none" ];
  services.openssh = {
    enable = true;
    settings.PermitRootLogin = installed.services.openssh.settings.PermitRootLogin;
    authorizedKeysFiles = installed.services.openssh.authorizedKeysFiles;
    extraConfig = installed.services.openssh.extraConfig;
  };
  users.mutableUsers = installed.users.mutableUsers;
  users.users.root.hashedPasswordFile = installed.users.users.root.hashedPasswordFile;
  users.users.reader.isNormalUser = true;
  environment.systemPackages = [ pkgs.mkpasswd pkgs.pamtester pkgs.openssh pkgs.sshpass ];
  virtualisation.emptyDiskImages = [ 128 ];
  virtualisation.fileSystems."/floatlab/system/etc" = {
    device = "/dev/vdb";
    fsType = "ext4";
    autoFormat = true;
    neededForBoot = installed.fileSystems."/floatlab/system/etc".neededForBoot;
  };
}
