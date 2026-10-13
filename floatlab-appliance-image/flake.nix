{
  description = "Immutable FloatLab NixOS appliance image";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
      floatlab-version = let version = builtins.getEnv "FLOATLAB_INSTALLER_VERSION";
        in if version != "" then version else pkgs.lib.removeSuffix "-dirty" (self.shortRev or self.dirtyShortRev or "unknown");
      floatlab-binaries = pkgs.buildGoModule {
        pname = "floatlab";
        version = "0.1.0";
        src = pkgs.lib.cleanSourceWith {
          src = ../.;
          filter = path: type:
            pkgs.lib.cleanSourceFilter path type
            && !pkgs.lib.hasPrefix (toString ./.) (toString path)
            && !pkgs.lib.hasPrefix (toString ../integration) (toString path)
            && builtins.baseNameOf path != "node_modules";
        };
        subPackages = [ "cmd/floatlab-hostd" "cmd/floatlab-control" "cmd/install-tui" "cmd/install-service" "cmd/install-service-mock" ];
        vendorHash = "sha256-P7u7guQRMa0D3Zlm79PqLU92oZFHuXMhClurkAAyd9s=";
      };
      floatlab-control-image = pkgs.dockerTools.buildLayeredImage {
        name = "floatlab-control";
        tag = "local";
        contents = [ floatlab-binaries pkgs.cacert pkgs.wget ];
        extraCommands = "mkdir -m 1777 tmp";
        config.Entrypoint = [ "/bin/floatlab-control" ];
      };
      makeISO = configuration:
        let
          cfg = configuration.config;
          kernel = "boot${cfg.boot.kernelPackages.kernel}/${cfg.system.boot.loader.kernelFile}";
          initrd = "boot${cfg.system.build.initialRamdisk}/${cfg.system.boot.loader.initrdFile}";
          squashfs = pkgs.callPackage (nixpkgs + "/nixos/lib/make-squashfs.nix") {
            storeContents = cfg.isoImage.storeContents;
            comp = cfg.isoImage.squashfsCompression;
          };
          manifest = pkgs.runCommand "floatlab-manifest.json" { nativeBuildInputs = [ pkgs.python3 ]; } ''
            python3 - <<'PYMANIFEST' > "$out"
            import hashlib, json, os
            files = {
                "${kernel}": "${cfg.boot.kernelPackages.kernel}/${cfg.system.boot.loader.kernelFile}",
                "${initrd}": "${cfg.system.build.initialRamdisk}/${cfg.system.boot.loader.initrdFile}",
                "nix-store.squashfs": "${squashfs}",
            }
            hashes = {}
            for name, source in files.items():
                with open(source, "rb") as stream:
                    hashes[name] = hashlib.file_digest(stream, "sha256").hexdigest()
            print(json.dumps({"kernel": "${kernel}", "initrd": "${initrd}", "init": "${cfg.system.build.toplevel}/init",
                              "files": hashes, "bytes": sum(os.stat(p).st_size for p in files.values())}))
            PYMANIFEST
          '';
        in cfg.system.build.isoImage.overrideAttrs (old: {
          sources = old.sources ++ [ manifest ];
          targets = old.targets ++ [ "/floatlab-manifest.json" ];
          squashfsCommand = "cp ${squashfs} \"$out\"";
        });
    in {
      nixosConfigurations.floatlab-iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./nixos/iso.nix ];
        specialArgs = { inherit floatlab-binaries floatlab-control-image floatlab-version; floatlab-development = false; };
      };

      nixosConfigurations.floatlab-dev-iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./nixos/iso.nix ];
        specialArgs = { inherit floatlab-binaries floatlab-control-image floatlab-version; floatlab-development = true; };
      };
      packages.${system} = {
        iso = makeISO self.nixosConfigurations.floatlab-iso;
        dev-iso = makeISO self.nixosConfigurations.floatlab-dev-iso;
        installer-test = pkgs.writeShellApplication {
          name = "floatlab-installer-test";
          runtimeInputs = [ pkgs.qemu (pkgs.python3.withPackages (p: [ p.pexpect ])) ];
          text = ''
            export FLOATLAB_OVMF=${pkgs.OVMF.fd}/FV
            exec python3 ${./tests/installer-vm.py} "$@"
          '';
        };
        inherit floatlab-binaries floatlab-control-image;
        default = self.packages.${system}.iso;
      };

      checks.${system} = {
        host-network = import ./tests/network.nix { inherit pkgs floatlab-binaries; };
        installer-boot-config =
        let cfg = self.nixosConfigurations.floatlab-iso.config;
        in assert cfg.fileSystems."/iso".fsType == "auto";
           assert cfg.fileSystems."/iso".device == "/dev/root";
           assert builtins.elem "ext4" cfg.boot.initrd.kernelModules;
           assert !cfg.boot.initrd.systemd.enable;
           assert cfg.networking.hostName == "";
           assert cfg.systemd.services."floatlab-installer-console@".serviceConfig.RestartForceExitStatus == "SIGHUP";
           assert pkgs.lib.hasSuffix "/bin/install-tui" cfg.systemd.services."floatlab-installer-console@".serviceConfig.ExecStart;
           assert cfg.systemd.services."floatlab-installer-console@".serviceConfig.TimeoutStopSec == "infinity";
           assert !(builtins.hasAttr "floatlab-install-worker" cfg.systemd.services);
           assert builtins.elem "--interface=floatlab-lan:routable" cfg.systemd.network.wait-online.extraArgs;
           pkgs.runCommand "floatlab-installer-boot-config" {} "touch $out";
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = with pkgs; [ just qemu_kvm libvirt virt-manager coreutils jq ];
      };
    };
}
