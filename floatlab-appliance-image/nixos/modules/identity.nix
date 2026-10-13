{ pkgs, ... }:
let
  identity = pkgs.writeShellApplication {
    name = "floatlab-restore-identity";
    runtimeInputs = [ pkgs.coreutils pkgs.jq pkgs.systemd pkgs.zfs ];
    text = ''
      if [ -f /floatlab-host-id ]; then
        id=$(cat /floatlab-host-id)
        [[ "$id" =~ ^[0-9a-f]{8}$ ]]
        zgenhostid -f -o /run/floatlab-hostid "$id"
        rm -f /etc/hostid
        cp /run/floatlab-hostid /etc/hostid
      fi
      if [ -f /floatlab/system/etc/appliance.json ]; then
        hostnamectl --transient set-hostname "$(jq -er .node_name /floatlab/system/etc/appliance.json)"
        timedatectl set-timezone "$(jq -er .timezone /floatlab/system/etc/appliance.json)"
      fi
    '';
  };
in {
  systemd.services.floatlab-host-identity = {
    after = [ "floatlab-datasets.service" ];
    requires = [ "floatlab-datasets.service" ];
    before = [ "floatlab-network-config.service" ];
    wantedBy = [ "multi-user.target" ];
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; ExecStart = "${identity}/bin/floatlab-restore-identity"; };
  };
  systemd.services.floatlab-network-config = {
    requires = [ "floatlab-host-identity.service" ];
    after = [ "floatlab-host-identity.service" ];
  };
}
