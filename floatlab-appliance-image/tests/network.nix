{ pkgs, floatlab-binaries, hostd-test-rpc }:
let
  network = node: { lib, ... }:
    let
      mac = link: "52:54:00:12:0${toString link}:0${toString node}";
    in {
      systemd.network.links = builtins.listToAttrs (map (link: {
        name = "10-test${toString link}";
        value = { matchConfig.MACAddress = mac link; linkConfig.Name = "test${toString link}"; };
      }) [ 0 1 2 3 ]);
      virtualisation.qemu.networkingOptions = lib.mkForce ([
        "-netdev user,id=management,restrict=on"
        "-device virtio-net-pci,netdev=management,mac=${mac 0}"
      ] ++ lib.concatMap (link: [
        "-device virtio-net-pci,netdev=lan${toString link},mac=${mac link}"
        ''-netdev vde,id=lan${toString link},sock="$FLOATLAB_LINK_${toString link}"''
      ]) [ 1 2 3 ]);
      # LACP requires a known full-duplex link speed; virtio defaults to unknown.
      virtualisation.qemu.options = [ "-global virtio-net-pci.speed=1000" "-global virtio-net-pci.duplex=full" ];
    };
in {
  host = { lib, ... }: {
    imports = [ ../nixos/modules/networking.nix (network 1) ];
    _module.args = { inherit floatlab-binaries; integrationCoverage = false; };
    networking.enableIPv6 = false;
    virtualisation.memorySize = 1024;
    environment.systemPackages = [ hostd-test-rpc pkgs.iproute2 ];
    systemd.services.floatlab-datasets = {
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        ExecStart = "${pkgs.coreutils}/bin/mkdir -p /floatlab/system/etc";
      };
    };
    systemd.services.floatlab-hostd = {
      wantedBy = [ "multi-user.target" ];
      requires = [ "floatlab-network-config.service" ];
      after = [ "floatlab-network-config.service" ];
      path = [ pkgs.systemd pkgs.ethtool ];
      serviceConfig = {
        ExecStart = "${floatlab-binaries}/bin/floatlab-hostd";
        Restart = "on-failure";
        RestartSec = 1;
      };
    };
    networking.firewall.enable = false;
    networking.interfaces = lib.mkForce {};
  };
  peer = { ... }: {
    imports = [ (network 2) ];
    networking.enableIPv6 = false;
    networking.useDHCP = false;
    networking.firewall.enable = false;
    networking.useNetworkd = true;
    systemd.network = {
      enable = true;
      netdevs = {
        "10-lan" = { netdevConfig = { Name = "lan"; Kind = "bridge"; }; bridgeConfig.STP = true; };
        "11-bond" = { netdevConfig = { Name = "bond0"; Kind = "bond"; }; bondConfig = { Mode = "802.3ad"; MIIMonitorSec = "100ms"; LACPTransmitRate = "fast"; }; };
      };
      networks = {
        "10-lan" = { matchConfig.Name = "lan"; address = [ "192.0.2.1/24" ]; networkConfig.ConfigureWithoutCarrier = true; };
        "11-direct" = { matchConfig.Name = "test1"; networkConfig.Bridge = "lan"; };
        "12-bond" = { matchConfig.Name = "bond0"; networkConfig.Bridge = "lan"; };
        "13-members" = { matchConfig.Name = "test2 test3"; networkConfig.Bond = "bond0"; };
      };
    };
    services.dnsmasq = {
      enable = true;
      settings = { interface = "lan"; bind-interfaces = true; dhcp-range = "192.0.2.50,192.0.2.100,255.255.255.0,1m"; dhcp-option = [ "option:router,192.0.2.1" "option:dns-server,192.0.2.1" ]; };
    };
  };
}
