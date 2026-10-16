{ lib, pkgs, modulesPath, ... }:
{
  imports = [
    (modulesPath + "/virtualisation/qemu-vm.nix")
    ./modules/installer-display.nix
  ];

  networking.hostName = "floatlab-installer-preview";
  system.stateVersion = "26.05";
  documentation.enable = false;
  virtualisation = {
    memorySize = 1536;
    cores = 2;
    diskImage = null;
    resolution = { x = 1600; y = 1000; };
    sharedDirectories.installer-preview = {
      source = "/workspace/.local/installer/vm/bin";
      target = "/run/installer-preview";
      securityModel = "none";
    };
    qemu.options = [
      "-vga none"
      "-device virtio-vga,xres=1600,yres=1000"
      "-display none"
      "-vnc 0.0.0.0:0"
      "-serial file:/workspace/.local/installer/vm/serial.log"
      "-qmp unix:/tmp/floatlab-preview-qmp.sock,server=on,wait=off"
      "-device virtio-serial"
      "-chardev socket,path=/tmp/floatlab-preview-guest.sock,server=on,wait=off,id=agent"
      "-device virtserialport,chardev=agent,name=org.qemu.guest_agent.0"
    ];
  };
  boot.kernelParams = [ "quiet" "loglevel=3" ];
  services.getty.autologinUser = "root";
  services.qemuGuest.enable = true;
  systemd.services.floatlab-installer-display = {
    wantedBy = [ "multi-user.target" ];
    requires = [ "run-installer\\x2dpreview.mount" ];
    after = [ "run-installer\\x2dpreview.mount" ];
    serviceConfig.Restart = lib.mkForce "always";
  };
}
