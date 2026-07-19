{
  description = "FloatLab Core development shell";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    {
      self,
      nixpkgs,
      flake-utils,
    }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs { inherit system; };
      in
      {
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            cloud-utils
            cdrkit
            curl
            libvirt
            openssh
            qemu
            virt-manager
            wget
          ];

          shellHook = ''
            echo "FloatLab VM tools available."
            echo "Host libvirt still needs to be installed, enabled, and accessible to your user."
          '';
        };
      }
    );
}
