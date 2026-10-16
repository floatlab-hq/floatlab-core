let
  lock = builtins.fromJSON (builtins.readFile ./flake.lock);
  nixpkgs = builtins.getFlake "github:NixOS/nixpkgs/${lock.nodes.nixpkgs.locked.rev}";
  host = import nixpkgs { system = builtins.currentSystem; };
  outputs = (import ./flake.nix).outputs { inherit nixpkgs; self = outputs; };
  vm = outputs.packages.x86_64-linux.installer-preview;
  runner = host.writeShellApplication {
  name = "floatlab-installer-preview";
  runtimeInputs = [ host.python3Packages.websockify ];
  text = ''
    cd /workspace/.local/installer/vm
    websockify --web=${host.novnc}/share/webapps/novnc 6080 127.0.0.1:5900 &
    trap 'kill "$!"' EXIT
    ${vm}/bin/run-floatlab-installer-preview-vm
  '';
  };
in host.symlinkJoin {
  name = "floatlab-installer-preview-tools";
  paths = [ runner host.python3 ];
}
