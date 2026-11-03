{ lib, pkgs, floatlab-binaries, integrationCoverage ? false, ... }:
{
  networking.useNetworkd = true;
  networking.useDHCP = lib.mkForce false;
  networking.networkmanager.enable = lib.mkForce false;
  networking.firewall.allowedTCPPorts = [ 8080 ];
  systemd.network.enable = true;
  systemd.network.wait-online.extraArgs = [ "--interface=floatlab-lan:routable" "--ipv4" ];
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
    requires = [ "floatlab-datasets.service" "systemd-networkd.service" "systemd-resolved.service" ] ++ lib.optional integrationCoverage "floatlab-integration-coverage.service";
    after = [ "floatlab-datasets.service" "systemd-networkd.service" "systemd-resolved.service" ] ++ lib.optional integrationCoverage "floatlab-integration-coverage.service";
    environment = lib.optionalAttrs integrationCoverage { GOCOVERDIR = "/floatlab/integration-coverage"; };
    before = [ "network-online.target" "floatlab-hostd.service" "floatlab-core-stack.service" ];
    path = with pkgs; [ coreutils systemd ethtool ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${floatlab-binaries}/bin/floatlab-hostd network init";
      TimeoutStartSec = "60s";
    };
  };
  systemd.services.floatlab-integration-coverage = lib.mkIf integrationCoverage {
    description = "Prepare persistent integration coverage outside rollback datasets";
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    before = [ "floatlab-network-config.service" "floatlab-hostd.service" ];
    path = [ pkgs.zfs pkgs.coreutils ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
    script = ''
      if ! zfs list -H floatlab/integration-coverage >/dev/null 2>&1; then
        zfs create -o mountpoint=/floatlab/integration-coverage floatlab/integration-coverage
      fi
      if [ "$(zfs get -H -o value mounted floatlab/integration-coverage)" != yes ]; then
        zfs mount floatlab/integration-coverage
      fi
      mkdir -p /floatlab/integration-coverage
      chmod 1777 /floatlab/integration-coverage
    '';
  };
}
