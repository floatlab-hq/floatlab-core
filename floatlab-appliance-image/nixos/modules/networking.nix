{ lib, pkgs, floatlab-binaries, ... }:
{
  networking.useNetworkd = true;
  networking.useDHCP = lib.mkForce false;
  networking.networkmanager.enable = lib.mkForce false;
  networking.firewall.allowedTCPPorts = [ 8080 ];
  systemd.network.enable = true;
  services.resolved.enable = true;

  # Hostd owns topology and static/service addresses. Networkd handles only the
  # dynamically generated DHCP configuration matching floatlab-lan.
  systemd.network.config.networkConfig = {
    ManageForeignRoutes = false;
    ManageForeignRoutingPolicyRules = false;
  };

  systemd.services.floatlab-network-config = {
    description = "Reconcile FloatLab host networking from network.toml";
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-datasets.service" "systemd-networkd.service" "systemd-resolved.service" ];
    after = [ "floatlab-datasets.service" "systemd-networkd.service" "systemd-resolved.service" ];
    before = [ "network-online.target" "floatlab-hostd.service" "floatlab-core-stack.service" ];
    path = with pkgs; [ coreutils systemd ethtool ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${floatlab-binaries}/bin/floatlab-hostd network init";
      TimeoutStartSec = "60s";
    };
  };
}
