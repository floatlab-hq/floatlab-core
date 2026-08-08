{ pkgs, ... }:
let
  configure = pkgs.writeShellApplication {
    name = "floatlab-configure-network";
    runtimeInputs = [ pkgs.systemd pkgs.coreutils ];
    text = ''
      set -euo pipefail
      config_dir=/floatlab/system/etc/systemd/network

      if ! find "$config_dir" -maxdepth 1 -type f \
          \( -name '*.network' -o -name '*.netdev' -o -name '*.link' \) \
          -print -quit | grep -q .; then
        echo "No persistent systemd-networkd configuration found" >&2
        printf '%s\n' network-config-missing > /run/floatlab/boot-mode
        exit 1
      fi

      networkctl reload
      systemctl restart systemd-networkd.service
      networkctl --no-pager status || true
    '';
  };
in {
  networking.useNetworkd = true;
  networking.useDHCP = false;
  networking.firewall.allowedTCPPorts = [ 8080 ];
  systemd.network.enable = true;

  # The persistent files should define a bridge (normally br0), nominate the
  # physical uplink, and assign the node's static address to the bridge.
  systemd.services.floatlab-network-config = {
    description = "Load persistent FloatLab network configuration";
    wantedBy = [ "multi-user.target" ];
    requires = [ "floatlab-datasets.service" "floatlab-dev-seed-network.service" ];
    after = [ "floatlab-datasets.service" "floatlab-dev-seed-network.service" "systemd-networkd.service" ];
    before = [ "network-online.target" "floatlab-core-stack.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${configure}/bin/floatlab-configure-network";
    };
  };
}
