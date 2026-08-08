{
  description = "Immutable FloatLab NixOS appliance image";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";
  };

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = import nixpkgs { inherit system; };
      floatlab-binaries = pkgs.buildGoModule {
        pname = "floatlab";
        version = "0.1.0";
        src = pkgs.lib.cleanSourceWith {
          src = ../.;
          filter = path: type:
            pkgs.lib.cleanSourceFilter path type
            && !pkgs.lib.hasPrefix (toString ./.) (toString path)
            && !pkgs.lib.hasPrefix (toString ../integration) (toString path);
        };
        subPackages = [ "cmd/floatlab-hostd" "cmd/floatlab-control" ];
        vendorHash = "sha256-H4NtzJzlurs58QjcEqEnJMx+avNiYcyGT3m4u58QZFs=";
      };
      floatlab-control-image = pkgs.dockerTools.buildLayeredImage {
        name = "floatlab-control";
        tag = "local";
        contents = [ floatlab-binaries pkgs.cacert pkgs.wget ];
        extraCommands = "mkdir -m 1777 tmp";
        config.Entrypoint = [ "/bin/floatlab-control" ];
      };
    in {
      nixosConfigurations.floatlab-iso = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [ ./nixos/iso.nix ];
        specialArgs = { inherit floatlab-binaries floatlab-control-image; };
      };

      packages.${system} = {
        iso = self.nixosConfigurations.floatlab-iso.config.system.build.isoImage;
        inherit floatlab-binaries floatlab-control-image;
        default = self.packages.${system}.iso;
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = with pkgs; [ just qemu_kvm libvirt virt-manager coreutils jq ];
      };
    };
}
