let
  lock = builtins.fromJSON (builtins.readFile ./flake.lock);
  nixpkgs = builtins.getFlake "github:NixOS/nixpkgs/${lock.nodes.nixpkgs.locked.rev}";
  host = import nixpkgs { system = builtins.currentSystem; };
  outputs = (import ./flake.nix).outputs { inherit nixpkgs; self = outputs; };
  vm = outputs.packages.x86_64-linux.installer-preview;
  novnc = host.runCommand "floatlab-novnc-assets" {} ''
    mkdir -p "$out"
    cd ${host.novnc.src}
    cp -r app core po vendor vnc.html vnc_lite.html "$out/"
  '';
in host.writeShellApplication {
  name = "floatlab-installer-preview";
  text = ''
    cd /workspace/.local/installer/vm
    ./installer-vm-control serve --web=${novnc} &
    trap 'kill "$!"' EXIT
    ${vm}/bin/run-floatlab-installer-preview-vm
  '';
}
