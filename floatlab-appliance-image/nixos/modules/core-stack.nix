{ floatlab-control-image, pkgs, ... }:
let
  seedCompose = pkgs.writeShellApplication {
    name = "floatlab-seed-core-stack";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      set -euo pipefail
      target=/floatlab/system/docker-compose.yml
      if [ ! -e "$target" ]; then
        install -m 0644 ${../../files/core-stack/docker-compose.yml} "$target"
      fi
    '';
  };

  startStack = pkgs.writeShellApplication {
    name = "floatlab-start-core-stack";
    runtimeInputs = [ pkgs.curl pkgs.docker pkgs.docker-compose pkgs.coreutils pkgs.gnugrep ];
    text = ''
      set -euo pipefail
      for _ in $(seq 1 30); do
        [ -S /run/floatlab/hostd.sock ] && break
        sleep 1
      done
      [ -S /run/floatlab/hostd.sock ]

      docker load --input ${floatlab-control-image} >/dev/null
      cd /floatlab/system
      docker compose -f docker-compose.yml up -d --remove-orphans

      for _ in $(seq 1 180); do
        curl -fsS http://127.0.0.1:8080/api/v1/health >/dev/null 2>&1 && break
        sleep 1
      done
      curl -fsS http://127.0.0.1:8080/api/v1/health >/dev/null
      if ! curl -fsS http://127.0.0.1:8080/api/v1/nodes | grep -q '"id":"node1"'; then
        curl -fsS -X POST http://127.0.0.1:8080/api/v1/nodes \
          -H 'Content-Type: application/json' \
          -d '{"id":"node1","name":"floatlab"}' >/dev/null
      fi
      curl -fsS http://127.0.0.1:8080/api/v1/nodes/node1/health | grep -q '"status":"online"'
    '';
  };
in {
  systemd.services.floatlab-core-stack-seed = {
    description = "Seed the FloatLab core Compose project when absent";
    requires = [ "floatlab-datasets.service" ];
    after = [ "floatlab-datasets.service" ];
    before = [ "floatlab-core-stack.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${seedCompose}/bin/floatlab-seed-core-stack";
    };
  };

  systemd.services.floatlab-core-stack = {
    description = "Start the FloatLab core management stack";
    wantedBy = [ "multi-user.target" ];
    requires = [
      "docker.service"
      "floatlab-core-stack-seed.service"
      "floatlab-hostd.service"
      "floatlab-network-config.service"
    ];
    after = [
      "docker.service"
      "floatlab-core-stack-seed.service"
      "floatlab-hostd.service"
      "floatlab-network-config.service"
      "network-online.target"
    ];
    wants = [ "network-online.target" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${startStack}/bin/floatlab-start-core-stack";
      ExecStop = "${pkgs.docker-compose}/bin/docker-compose -f /floatlab/system/docker-compose.yml down";
      TimeoutStartSec = "10min";
    };
  };
}
