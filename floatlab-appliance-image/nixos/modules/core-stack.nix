{ pkgs, floatlab-development ? false, floatlab-dev-bundle ? null, ... }:
let
  devCompose = if floatlab-dev-bundle == null then null else pkgs.writeText "floatlab-dev-compose.yml" (
    builtins.replaceStrings
      [ "      FLOATLAB_APPLIANCE_CONFIG: /floatlab/system/etc/appliance.json\n      FLOATLAB_CREDENTIAL_KEY_FILE: /floatlab/system/etc/credential.key\n      FLOATLAB_BOOTSTRAP_FILE: /floatlab/system/etc/bootstrap.json" ]
      [ "      FLOATLAB_BOOTSTRAP_USER: demo\n      FLOATLAB_BOOTSTRAP_PASSWORD: floatlab\n      FLOATLAB_JWT_SECRET: floatlab-dev" ]
      (builtins.readFile (floatlab-dev-bundle + "/docker-compose.yml"))
  );
  seedCompose = pkgs.writeShellApplication {
    name = "floatlab-seed-core-stack";
    runtimeInputs = [ pkgs.coreutils ];
    text = ''
      ${pkgs.lib.optionalString (floatlab-development && floatlab-dev-bundle != null) ''
        if [ ! -e /floatlab/system/docker-compose.yml ]; then
          install -d /floatlab/system/images
          cp ${floatlab-dev-bundle}/images/*.tar /floatlab/system/images/
          cp ${floatlab-dev-bundle}/images.json /floatlab/system/images.json
          cp ${devCompose} /floatlab/system/docker-compose.yml
        fi
      ''}
      test -s /floatlab/system/docker-compose.yml
      test -s /floatlab/system/images.json
    '';
  };

  startStack = pkgs.writeShellApplication {
    name = "floatlab-start-core-stack";
    runtimeInputs = [ pkgs.curl pkgs.docker pkgs.docker-compose pkgs.coreutils pkgs.gnugrep pkgs.jq ];
    text = ''
      set -euo pipefail
      ${seedCompose}/bin/floatlab-seed-core-stack
      for _ in $(seq 1 30); do
        [ -S /run/floatlab/hostd.sock ] && break
        sleep 1
      done
      [ -S /run/floatlab/hostd.sock ]

      while IFS=$'\t' read -r archive image expected; do
        if [ "$(docker image inspect --format '{{.Id}}' "$image" 2>/dev/null || true)" != "$expected" ]; then
          docker load --input "/floatlab/system/images/$archive"
        fi
        test "$(docker image inspect --format '{{.Id}}' "$image")" = "$expected"
      done < <(jq -er '.[] | [.archive,.image,.id] | @tsv' /floatlab/system/images.json)
      cd /floatlab/system
      composeArgs=(-f docker-compose.yml)
      if [ -f /floatlab/system/etc/development ]; then
        composeArgs+=(-f ${../../files/core-stack/docker-compose.dev.yml})
      fi
      docker compose "''${composeArgs[@]}" up -d --pull never --remove-orphans
      # Do not report success merely because the management process is listening.
      for service in rqlite victoria-metrics victoria-logs floatlab-control; do
        ready=false
        for _ in $(seq 1 180); do
          id=$(docker compose "''${composeArgs[@]}" ps -q "$service")
          status=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id" 2>/dev/null || true)
          if [ "$status" = healthy ] || [ "$status" = running ]; then ready=true; break; fi
          sleep 1
        done
        "$ready"
      done
      curl -fsS http://127.0.0.1:8428/health >/dev/null
      curl -fsS http://127.0.0.1:9428/health >/dev/null

      for _ in $(seq 1 180); do
        curl -fsS http://127.0.0.1:8080/api/v1/health >/dev/null 2>&1 && break
        sleep 1
      done
      curl -fsS http://127.0.0.1:8080/api/v1/health >/dev/null

    '';
  };
in {
  environment.systemPackages = [ startStack (pkgs.writeShellApplication {
    name = "floatlab-mode";
    runtimeInputs = [ pkgs.coreutils pkgs.util-linux pkgs.docker pkgs.systemd startStack ];
    text = builtins.readFile ../../../scripts/floatlab-mode.sh;
  }) ];
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
      "floatlab-network-config.service"
    ];
    after = [
      "docker.service"
      "floatlab-core-stack-seed.service"
      "floatlab-hostd.service"
      "floatlab-network-config.service"
      "network-online.target"
    ];
    # Explicit hostd restarts must not tear down the management containers.
    wants = [ "network-online.target" "floatlab-hostd.service" ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = "${startStack}/bin/floatlab-start-core-stack";
      ExecStop = "${pkgs.docker-compose}/bin/docker-compose -f /floatlab/system/docker-compose.yml down";
      TimeoutStartSec = "10min";
      Restart = "on-failure";
      RestartSec = "10s";
    };
  };
}
