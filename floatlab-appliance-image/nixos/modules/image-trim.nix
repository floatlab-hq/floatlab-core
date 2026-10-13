{ config, lib, pkgs, ... }:
let
  kernel = config.boot.kernelPackages.kernel;
  modules = pkgs.runCommand "floatlab-kernel-modules" {
    nativeBuildInputs = [ pkgs.kmod ];
    allowedReferences = [ "out" ];
  } ''
    cp -r --no-preserve=mode ${lib.getOutput "modules" kernel} "$out"
    tree="$out/lib/modules/${kernel.modDirVersion}"
    rm -rf "$tree/kernel/sound" "$tree/kernel/drivers/media" \
      "$tree/kernel/drivers/bluetooth" "$tree/kernel/net/bluetooth" \
      "$tree/kernel/drivers/net/wireless" "$tree/kernel/net/wireless" \
      "$tree/kernel/net/mac80211" "$tree/kernel/drivers/soundwire"
    sed -i '\|kernel/sound/|d; \|kernel/drivers/media/|d; \|bluetooth/|d; \|wireless/|d; \|mac80211/|d; \|soundwire/|d' "$tree/modules.order"
    depmod -b "$out" ${kernel.modDirVersion}
    test -f "$tree/kernel/drivers/nvme/host/nvme.ko.xz"
    test -f "$tree/kernel/drivers/net/virtio_net.ko.xz"
    test ! -d "$tree/kernel/sound"
    test ! -d "$tree/kernel/drivers/net/wireless"
  '';
  firmware = pkgs.runCommand "floatlab-firmware" {
    nativeBuildInputs = [ pkgs.kmod ];
    allowedReferences = [ "out" ];
    passthru.compressFirmware = false;
  } ''
    tree=${modules}/lib/modules/${kernel.modDirVersion}/kernel
    find "$tree/drivers" -type f \
      \( -path '*/net/*' -o -path '*/scsi/*' -o -path '*/ata/*' \
         -o -path '*/nvme/*' -o -path '*/block/*' -o -path '*/mmc/*' \
         -o -path '*/usb/host/*' -o -path '*/usb/storage/*' \) -print0 |
      xargs -0 -r -n 1 modinfo -F firmware | sort -u > requested-firmware
    mkdir -p "$out/lib/firmware"
    cd ${pkgs.compressFirmwareZstd pkgs.linux-firmware}/lib/firmware
    shopt -s nullglob
    while IFS= read -r pattern; do
      # Drivers also request optional firmware not supplied by linux-firmware.
      for file in $pattern $pattern.zst $pattern.xz; do
        [ -f "$file" ] || continue
        cp -L --parents --no-preserve=mode "$file" "$out/lib/firmware"
      done
    done < "$NIX_BUILD_TOP/requested-firmware"
    cp -rL --no-preserve=mode amd-ucode "$out/lib/firmware/"
    test -f "$out/lib/firmware/rtl_nic/rtl8168e-1.fw.zst"
    test -f "$out/lib/firmware/amd-ucode/microcode_amd.bin.zst"
    test ! -e "$out/lib/firmware/iwlwifi-9000-pu-b0-jf-b0-46.ucode.zst"
  '';
in {
  # The stock installer adds these independently of our base package list.
  options.environment.systemPackages = lib.mkOption {
    apply = packages:
      lib.filter (package: !(builtins.elem (lib.getName package) [ "vim" "w3m" ])) packages;
  };

  config = {
    documentation.enable = lib.mkForce false;
    documentation.nixos.enable = lib.mkForce false;
    documentation.man.enable = lib.mkForce false;
    documentation.doc.enable = lib.mkForce false;
    documentation.info.enable = lib.mkForce false;

    hardware.enableRedistributableFirmware = lib.mkForce false;
    hardware.wirelessRegulatoryDatabase = lib.mkForce false;
    hardware.firmware = lib.mkForce [ firmware ];
    hardware.cpu.intel.updateMicrocode = true;
    hardware.cpu.amd.updateMicrocode = true;
    hardware.bluetooth.enable = lib.mkForce false;
    networking.wireless.enable = lib.mkForce false;
    services.pipewire.enable = lib.mkForce false;
    services.pulseaudio.enable = lib.mkForce false;

    # Copy retained modules so the full module output cannot enter the closure.
    system.modulesTree = lib.mkForce ([ modules ] ++ config.boot.extraModulePackages);
  };
}
