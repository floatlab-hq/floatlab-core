{
  description = "Immutable FloatLab NixOS appliance image";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs {
        inherit system;
        overlays = [ (final: prev: {
          floatlab-zbm-kcl = prev.runCommand "floatlab-zbm-kcl" { nativeBuildInputs = [ prev.makeWrapper ]; } ''
            mkdir -p "$out/bin"
            cp ${prev.fetchurl {
              url = "https://github.com/zbm-dev/zfsbootmenu/releases/download/v3.1.0/zbm-kcl";
              sha256 = "16edae3eee5df9a0b133734bc4d8a8cb68ca73ac30f98cb21cea3212b051ff01";
            }} "$out/bin/zbm-kcl"
            chmod +x "$out/bin/zbm-kcl"
            substituteInPlace "$out/bin/zbm-kcl" --replace-fail '#!/bin/bash' '#!${prev.bash}/bin/bash'
            wrapProgram "$out/bin/zbm-kcl" --prefix PATH : ${prev.lib.makeBinPath [ prev.binutils prev.coreutils prev.gawk prev.gnugrep prev.gnused prev.zfs ]}
          '';
        }) ];
      };
      zfsbootmenu = pkgs.fetchurl {
        url = "https://github.com/zbm-dev/zfsbootmenu/releases/download/v3.1.0/zfsbootmenu-release-x86_64-v3.1.0-linux6.12.EFI";
        sha256 = "131d55c7b385df8cd319d267b6aa15d97f8c32c94f5149bd906556715fadf4e7";
      };
      floatlab-version = let version = builtins.getEnv "FLOATLAB_INSTALLER_VERSION";
        in if version != "" then version else pkgs.lib.removeSuffix "-dirty" (self.shortRev or self.dirtyShortRev or "unknown");
      integrationCoverage = builtins.getEnv "FLOATLAB_INTEGRATION_COVERAGE" == "1";
      hostVersion = let v = builtins.getEnv "FLOATLAB_HOST_VERSION"; in if v == "" then "0.1.0-dev" else v;
      buildBinaries = name: version: subPackages:
        let instrument = integrationCoverage && builtins.elem name [ "floatlab-host" "floatlab-manage" ];
        in pkgs.buildGoModule {
        pname = name;
        inherit version;
        ldflags = [ "-s" "-w" "-X github.com/floatlab/floatlab-core/pkg/release.Version=${version}" "-X github.com/floatlab/floatlab-core/pkg/release.GitCommit=${self.rev or self.dirtyRev or "unknown"}" ];
        src = pkgs.lib.cleanSourceWith {
          src = ../.;
          filter = path: type:
            pkgs.lib.cleanSourceFilter path type
            && !pkgs.lib.hasPrefix (toString ./.) (toString path)
            && !pkgs.lib.hasPrefix (toString ../integration) (toString path)
            && builtins.baseNameOf path != ".local"
            && builtins.baseNameOf path != "node_modules";
        };
        inherit subPackages;
        env.CGO_ENABLED = "0";
        tags = pkgs.lib.optionals instrument [ "integrationcoverage" ];
        preBuild = pkgs.lib.optionalString instrument ''
          export GOFLAGS="$GOFLAGS -cover -coverpkg=./... -covermode=atomic"
        '';
        vendorHash = "sha256-eri1Nsh2zHiXN65Npvb1FejZmNMMvNeNxgwjBvM2Qxs=";
      };
      floatlab-binaries = buildBinaries "floatlab-host" hostVersion [ "cmd/floatlab-hostd" ];
      floatlab-manage-binaries = buildBinaries "floatlab-manage" "0.1.0-dev" [ "cmd/floatlab-control" ];
      floatlab-installer-binaries = buildBinaries "floatlab-installer" floatlab-version [ "cmd/install-tui" "cmd/install-service" "cmd/install-service-mock" ];
      installer-test-binary = buildBinaries "floatlab-installer-test" floatlab-version [ "scripts/installer-vm-test" ];
      hostd-test-rpc = buildBinaries "floatlab-hostd-test-rpc" floatlab-version [ "scripts/hostd-test-rpc" ];
      vm-test-binary = buildBinaries "floatlab-nixos-vm-test" floatlab-version [ "scripts/nixos-vm-test" ];
      testVM = name: module:
        let
          vm = nixpkgs.lib.nixosSystem {
            inherit system pkgs;
            modules = [
              (nixpkgs + "/nixos/modules/virtualisation/qemu-vm.nix")
              {
                networking.hostName = name;
                system.stateVersion = "26.05";
                system.disableInstallerTools = true;
                documentation.enable = false;
                virtualisation = { graphics = false; qemu.package = pkgs.qemu_test; };
                services.qemuGuest.package = pkgs.qemu_test.ga;
              }
              module
            ];
          };
        in "${vm.config.system.build.vm}/bin/run-${name}-vm";
      vmCheck = name: mode: launchers:
        let
          driver = pkgs.writeShellApplication {
            name = "floatlab-${name}-test";
            runtimeInputs = pkgs.lib.optionals (mode == "network") [ pkgs.vde2 ];
            text = ''
              exec ${vm-test-binary}/bin/nixos-vm-test ${mode} "$@" ${pkgs.lib.escapeShellArgs launchers}
            '';
          };
        in pkgs.runCommand "floatlab-${name}-check" { passthru = { inherit driver; }; } ''
          mkdir -p "$out"
          ${pkgs.lib.getExe driver} --logs "$out"
        '';
      networkVMs = import ./tests/network.nix { inherit pkgs floatlab-binaries hostd-test-rpc; };
      shellCheck = test: ''
        export GOCACHE="$TMPDIR/go-cache" GOENV=off GO111MODULE=off
        export FLOATLAB_TEST_MODULES=${./nixos/modules}
        go test ${./tests/shellchecks}/*.go -run "^${test}$"
      '';
      installed = self.nixosConfigurations.floatlab-installed.config;
      closure = pkgs.closureInfo { rootPaths = [ installed.system.build.toplevel ]; };
      host-image = pkgs.runCommand "floatlab-host-${hostVersion}" { nativeBuildInputs = [ pkgs.gnutar pkgs.gzip pkgs.jq ]; } ''
        mkdir -p "$out" root/nix/store root/boot root/etc root/var root/run root/tmp
        chmod 1777 root/tmp
        while IFS= read -r path; do cp -a --parents "$path" root; done < ${closure}/store-paths
        cp ${closure}/registration root/nix-path-registration
        cp ${installed.boot.kernelPackages.kernel}/${installed.system.boot.loader.kernelFile} root/boot/vmlinuz
        cp ${installed.system.build.initialRamdisk}/${installed.system.boot.loader.initrdFile} root/boot/initramfs
        ln -s ${installed.system.build.toplevel} root/run/current-system
        ln -s ${installed.system.build.toplevel}/init root/init
        du -sb root | cut -f1 > "$out/root-bytes"
        tar --sort=name --mtime=@1 --owner=0 --group=0 --numeric-owner -C root -cf - . | gzip -n > "$out/floatlab-root.tar.gz"
        cp ${zfsbootmenu} "$out/zfsbootmenu.EFI"
        rootDigest=$(sha256sum "$out/floatlab-root.tar.gz" | cut -d" " -f1)
        bootDigest=$(sha256sum "$out/zfsbootmenu.EFI" | cut -d" " -f1)
        bytes=$(stat --format=%s "$out/floatlab-root.tar.gz" "$out/zfsbootmenu.EFI" | paste -sd+)
        jq -n --arg rootDigest "$rootDigest" --arg bootDigest "$bootDigest" \
          --argjson rootBytes "$(cat "$out/root-bytes")" --argjson bytes "$((bytes))" '
          {version: "${hostVersion}", git_commit: "${self.rev or self.dirtyRev or "unknown"}",
           recovery_protocol: 1, bootloader: "zfsbootmenu-3.1.0", pool_compatibility: "openzfs-2.3-linux",
           engine: {docker: "${pkgs.docker.version}", containerd: "${pkgs.containerd.version}", storage_driver: "zfs", data_root: "/floatlab/system/docker"},
           qualified_sources: ${builtins.toJSON ((builtins.fromJSON (builtins.readFile ../releases/host-compatibility.json)).qualified_transitions.${hostVersion} or [])},
           architecture: "amd64", init: "${installed.system.build.toplevel}/init",
           kernel: "${installed.boot.kernelPackages.kernel}", nixpkgs_rev: "${nixpkgs.rev}", root_bytes: $rootBytes,
           files: {"floatlab-root.tar.gz": $rootDigest, "zfsbootmenu.EFI": $bootDigest}, bytes: $bytes}
        ' > "$out/host-manifest.json"
      '';
      bundlePath = builtins.getEnv "FLOATLAB_ISO_BUNDLE";
      bundle = if bundlePath == "" then throw "Set FLOATLAB_ISO_BUNDLE using scripts/build-iso.sh" else builtins.path { path = /. + bundlePath; name = "floatlab-iso-bundle"; };
      bundleManifest = builtins.fromJSON (builtins.readFile (bundle + "/floatlab-manifest.json"));
      makeISO = configuration:
        let
          cfg = configuration.config;
          squashfs = pkgs.callPackage (nixpkgs + "/nixos/lib/make-squashfs.nix") {
            storeContents = cfg.isoImage.storeContents;
            comp = cfg.isoImage.squashfsCompression;
          };
        in assert bundleManifest.architecture == "amd64";
           assert bundleManifest.kernel == toString cfg.boot.kernelPackages.kernel;
           cfg.system.build.isoImage.overrideAttrs (old: {
          sources = old.sources ++ [ (bundle + "/floatlab-manifest.json") ] ++ (map (name: bundle + "/${name}") (builtins.attrNames bundleManifest.files));
          targets = old.targets ++ [ "/floatlab-manifest.json" ] ++ (map (name: "/${name}") (builtins.attrNames bundleManifest.files));
          squashfsCommand = "cp ${squashfs} \"$out\"";
        });
    in {
      nixosConfigurations.floatlab-iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./nixos/iso.nix ];
        pkgs = pkgs;
        specialArgs = { inherit floatlab-binaries floatlab-installer-binaries floatlab-version integrationCoverage; floatlab-installer-command = null; };
      };

      nixosConfigurations.floatlab-installed = nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        modules = [ ./nixos/installed.nix ];
        specialArgs = { inherit floatlab-binaries floatlab-installer-binaries floatlab-version integrationCoverage; };
      };
      nixosConfigurations.floatlab-installer-preview = nixpkgs.lib.nixosSystem {
        inherit system pkgs;
        modules = [ ./nixos/installer-preview.nix {
          virtualisation.host.pkgs = import nixpkgs { system = builtins.currentSystem; };
          virtualisation.qemu.package = (import nixpkgs { system = builtins.currentSystem; }).qemu;
        } ];
        specialArgs = {
          inherit floatlab-installer-binaries;
          floatlab-installer-command = "/run/installer-preview/install-tui --mock --service /run/installer-preview/install-service-mock";
        };
      };
      packages.${system} = {
        iso = makeISO self.nixosConfigurations.floatlab-iso;
        installer-preview = self.nixosConfigurations.floatlab-installer-preview.config.system.build.vm;
        installer-test = pkgs.writeShellApplication {
          name = "floatlab-installer-test";
          runtimeInputs = [ pkgs.qemu_test ];
          text = ''
            export FLOATLAB_OVMF=${pkgs.OVMF.fd}/FV
            exec ${installer-test-binary}/bin/installer-vm-test "$@"
          '';
        };
        inherit floatlab-binaries floatlab-manage-binaries floatlab-installer-binaries host-image;
        default = self.packages.${system}.iso;
      };

      checks.${system} = {
        power-management =
        let
          staysAwake = cfg:
            pkgs.lib.all (name: cfg.services.logind.settings.Login.${name} == "ignore")
              [ "IdleAction" "HandleLidSwitch" "HandleLidSwitchExternalPower" "HandleLidSwitchDocked" ]
            && pkgs.lib.all (name: cfg.systemd.sleep.settings.Sleep.${name} == false)
              [ "AllowSuspend" "AllowHibernation" "AllowHybridSleep" "AllowSuspendThenHibernate" ];
        in assert pkgs.lib.all (name: staysAwake self.nixosConfigurations.${name}.config)
             [ "floatlab-iso" "floatlab-installed" ];
           pkgs.runCommand "floatlab-power-management-check" {} ''
             touch "$out"
           '';
        installer-answers = pkgs.runCommand "floatlab-installer-answers-check" { nativeBuildInputs = [ pkgs.go pkgs.bash pkgs.coreutils ]; } ''
          ${shellCheck "TestInstallerAnswers"}
          touch "$out"
        '';
        zfs-memory = pkgs.runCommand "floatlab-zfs-memory-check" { nativeBuildInputs = [ pkgs.go pkgs.bash pkgs.gawk ]; } ''
          ${shellCheck "TestZFSMemory"}
          touch "$out"
        '';
        zfsbootmenu-efi = pkgs.runCommand "floatlab-zfsbootmenu-efi-check" {} ''
          mkdir -p "$out"
          export PATH=${pkgs.lib.makeBinPath [ pkgs.floatlab-zbm-kcl ]}
          zbm-kcl -r spl_hostid -r zbm.prefer -r zbm.import_policy -r zbm.timeout -r console \
            -a 'spl_hostid=0x20390125 zbm.prefer=floatlab!! zbm.import_policy=strict zbm.timeout=5 console=tty0 console=ttyS0,115200n8' \
            -o "$out/BOOTX64.EFI" ${zfsbootmenu}
          cmdline=$(zbm-kcl "$out/BOOTX64.EFI")
          case "$cmdline" in
            *'spl_hostid=0x20390125 zbm.prefer=floatlab!! zbm.import_policy=strict zbm.timeout=5 console=tty0 console=ttyS0,115200n8'*) ;;
            *) exit 1 ;;
          esac
        '';
        os-live = pkgs.runCommand "floatlab-os-live-check" { nativeBuildInputs = [ pkgs.go pkgs.bash ]; } ''
          ${shellCheck "TestOSLive"}
          touch "$out"
        '';
        installer-runtime =
        let
          cfg = self.nixosConfigurations.floatlab-iso.config;
          closure = pkgs.closureInfo { rootPaths = [ cfg.system.build.toplevel ]; };
        in pkgs.runCommand "floatlab-installer-runtime-check" { nativeBuildInputs = [ pkgs.go pkgs.bash ]; } ''
          ${shellCheck "TestInstallerRoot"}
          if grep -E -- '-(docker|docker-buildx|docker-compose|docker-containerd|docker-runc|moby)-[0-9]|-floatlab-control\.tar' ${closure}/store-paths; then
            echo "Container runtime remains in the live installer closure" >&2
            exit 1
          fi
          if grep -E -- '-(git|git-minimal)-[0-9]|-nixos-[0-9]' ${closure}/store-paths; then
            echo "Git or bundled Nixpkgs remains in the live installer closure" >&2
            exit 1
          fi
          touch "$out"
        '';
        host-network = vmCheck "host-network" "network" [
          (testVM "network-host" networkVMs.host)
          (testVM "network-peer" networkVMs.peer)
        ];
        recovery-password = vmCheck "recovery-password" "recovery" [
          (testVM "recovery" (import ./tests/recovery-password.nix { inherit pkgs installed; }))
        ];
        image-trim =
        let
          configurations = map (name: self.nixosConfigurations.${name}.config)
            [ "floatlab-iso" "floatlab-installed" ];
          trimmed = cfg:
            !cfg.documentation.enable && !cfg.services.rsyslogd.enable
            && !cfg.hardware.bluetooth.enable && !cfg.networking.wireless.enable
            && cfg.hardware.cpu.intel.updateMicrocode && cfg.hardware.cpu.amd.updateMicrocode
            && builtins.elem "nano" (map pkgs.lib.getName cfg.environment.systemPackages)
            && !(pkgs.lib.any (p: builtins.elem (pkgs.lib.getName p) [ "vim" "w3m" "rsyslog" ]) cfg.environment.systemPackages);
          closure = pkgs.closureInfo { rootPaths = map (cfg: cfg.system.build.toplevel) configurations; };
        in assert pkgs.lib.all trimmed configurations;
           pkgs.runCommand "floatlab-image-trim-check" {} ''
             if grep -E -- '-(linux-firmware|alsa-firmware|sof-firmware|libreelec-dvb-firmware|rsyslog|vim|w3m|nixos-manual-html)-' ${closure}/store-paths; then
               echo "Untrimmed packages remain in the appliance closure" >&2
               exit 1
             fi
             if grep -Fx '${pkgs.lib.getOutput "modules" (builtins.head configurations).boot.kernelPackages.kernel}' ${closure}/store-paths; then
               echo "The original kernel module tree remains in the appliance closure" >&2
               exit 1
             fi
             touch "$out"
           '';
        installer-boot-config =
        let
          cfg = self.nixosConfigurations.floatlab-iso.config;
          installed = self.nixosConfigurations.floatlab-installed.config;
        in assert cfg.fileSystems."/iso".fsType == "iso9660";
           assert !cfg.boot.initrd.systemd.enable;
           assert !cfg.virtualisation.docker.enable;
           assert !cfg.programs.git.enable;
           assert cfg.system.disableInstallerTools;
           assert !cfg.system.installer.channel.enable;
           assert cfg.nix.enable;
           assert installed.virtualisation.docker.enable;
           assert builtins.elem floatlab-binaries installed.environment.systemPackages;
           assert !installed.systemd.paths.floatlab-hostd-dev.unitConfig.DefaultDependencies;
           assert cfg.systemd.services.docker.serviceConfig.RootDirectory == "/run/floatlab-installer/root";
           assert cfg.systemd.services.docker.serviceConfig.PrivateMounts;
           assert !cfg.systemd.services.docker.serviceConfig.MountAPIVFS;
           assert cfg.systemd.services.floatlab-hostd.environment.SYSTEMD_OFFLINE == "0";
           assert cfg.systemd.services.floatlab-network-config.environment.SYSTEMD_OFFLINE == "0";
           assert builtins.elem "floatlab-installer-root.service" cfg.systemd.services.floatlab-core-stack.requires;
           assert installed.fileSystems."/".fsType == "zfs";
           assert installed.fileSystems."/".device == "floatlab/os/live";
           assert installed.fileSystems."/floatlab/system".neededForBoot;
           assert installed.fileSystems."/floatlab/system/etc".neededForBoot;
           assert installed.fileSystems."/floatlab/system/etc".device == "floatlab/system/etc";
           assert installed.users.users.root.hashedPasswordFile == "/floatlab/system/etc/root-password.hash";
           assert !installed.users.mutableUsers;
           assert installed.services.openssh.settings.PermitRootLogin == "prohibit-password";
           assert pkgs.lib.hasInfix "Match User root" installed.services.openssh.extraConfig;
           assert pkgs.lib.hasInfix "/floatlab/system/etc/root-authorized-keys" installed.services.openssh.extraConfig;
           assert !(pkgs.lib.hasInfix "/floatlab/system/etc/root-authorized-keys" cfg.services.openssh.extraConfig);
           assert map (key: key.path) installed.services.openssh.hostKeys == [
             "/floatlab/system/etc/ssh/ssh_host_rsa_key"
             "/floatlab/system/etc/ssh/ssh_host_ed25519_key"
           ];
           assert installed.environment.etc."ssh/ssh_host_ed25519_key.pub".source == "/floatlab/system/etc/ssh/ssh_host_ed25519_key.pub";
           assert builtins.elem "sshd-keygen.service" cfg.systemd.services.floatlab-installer-root.requires;
           assert builtins.elem "floatlab-datasets.service" installed.systemd.services.sshd-keygen.requires;
           assert builtins.elem "sshd-keygen.service" installed.systemd.services.floatlab-hostd.requires;
           assert installed.hardware.enableAllHardware;
           assert pkgs.lib.all (name: builtins.elem name installed.boot.initrd.availableKernelModules)
             [ "virtio_blk" "virtio_scsi" "nvme" "ahci" "uas" "hpsa" "hv_storvsc" ];
           assert !(builtins.hasAttr "/iso" installed.fileSystems);
           assert !installed.boot.loader.grub.enable;
           assert !installed.boot.zfs.forceImportRoot;
           assert !installed.boot.initrd.systemd.enable;
           assert pkgs.lib.hasInfix "os/live" installed.boot.initrd.postResumeCommands;
           assert !(builtins.hasAttr "floatlab-install-discovery" installed.systemd.services);
           assert cfg.networking.hostName == "";
           assert pkgs.lib.hasInfix "/bin/kmscon" cfg.systemd.services.floatlab-installer-display.serviceConfig.ExecStart;
           assert pkgs.lib.hasSuffix "/bin/install-tui" cfg.systemd.services.floatlab-installer-display.serviceConfig.ExecStart;
           assert cfg.systemd.services.floatlab-installer-display.environment.COLORTERM == "truecolor";
           assert builtins.elem "PATH=${cfg.systemd.services.floatlab-installer-display.environment.PATH}" (pkgs.lib.splitString " " cfg.systemd.services.floatlab-installer-display.serviceConfig.ExecStart);
           assert cfg.fonts.fontconfig.defaultFonts.monospace == [ "FloatLab Braille" "IBM Plex Mono" "Unifont" ];
           assert !cfg.isoImage.forceTextMode;
           assert cfg.systemd.services."floatlab-installer-console@".serviceConfig.RestartForceExitStatus == "SIGHUP";
           assert pkgs.lib.hasSuffix "/bin/install-tui" cfg.systemd.services."floatlab-installer-console@".serviceConfig.ExecStart;
           assert cfg.systemd.services."floatlab-installer-console@".serviceConfig.TimeoutStopSec == "infinity";
           assert cfg.systemd.services.floatlab-unattended-install.serviceConfig.Type == "oneshot";
           assert !(builtins.hasAttr "floatlab-install-worker" cfg.systemd.services);
           assert builtins.elem "--interface=floatlab-lan:routable" cfg.systemd.network.wait-online.extraArgs;
           pkgs.runCommand "floatlab-installer-boot-config" {} ''
             grep -F 'floatlab-unattended-install.service' ${cfg.systemd.services.floatlab-install-discovery.serviceConfig.ExecStart} >/dev/null
             grep -F '${pkgs.nftables}/bin' ${installed.systemd.services.floatlab-core-stack.serviceConfig.ExecStart} >/dev/null
             touch "$out"
           '';
      };

      devShells.${system}.default = pkgs.mkShell {
        FLOATLAB_OVMF = "${pkgs.OVMF.fd}/FV";
        packages = with pkgs; [ just qemu_kvm libvirt coreutils jq docker-client go gh xorriso bun pnpm ];
      };
    };
}
