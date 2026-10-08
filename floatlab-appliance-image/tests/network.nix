{ pkgs, floatlab-binaries }:
pkgs.testers.runNixOSTest {
  name = "floatlab-host-network";
  nodes = {
    host = { lib, ... }: {
      imports = [ ../nixos/modules/networking.nix ];
      _module.args.floatlab-binaries = floatlab-binaries;
      virtualisation.vlans = [ 1 2 3 ];
      # LACP requires a known full-duplex link speed; virtio defaults to unknown.
      virtualisation.qemu.options = [ "-global virtio-net-pci.speed=1000" "-global virtio-net-pci.duplex=full" ];
      networking.enableIPv6 = false;
      virtualisation.memorySize = 1024;
      environment.systemPackages = [ pkgs.python3 pkgs.iproute2 ];
      systemd.services.floatlab-datasets = {
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          ExecStart = "${pkgs.coreutils}/bin/mkdir -p /floatlab/system/etc";
        };
      };
      systemd.services.floatlab-hostd = {
        wantedBy = [ "multi-user.target" ];
        requires = [ "floatlab-network-config.service" ];
        after = [ "floatlab-network-config.service" ];
        path = [ pkgs.systemd pkgs.ethtool ];
        serviceConfig = {
          ExecStart = "${floatlab-binaries}/bin/floatlab-hostd";
          Restart = "on-failure";
          RestartSec = 1;
        };
      };
      networking.firewall.enable = false;
      networking.interfaces = lib.mkForce {};
    };
    peer = { ... }: {
      virtualisation.vlans = [ 1 2 3 ];
      # LACP requires a known full-duplex link speed; virtio defaults to unknown.
      virtualisation.qemu.options = [ "-global virtio-net-pci.speed=1000" "-global virtio-net-pci.duplex=full" ];
      networking.enableIPv6 = false;
      networking.useDHCP = false;
      networking.firewall.enable = false;
      networking.useNetworkd = true;
      systemd.network = {
        enable = true;
        netdevs = {
          "10-lan" = { netdevConfig = { Name = "lan"; Kind = "bridge"; }; bridgeConfig.STP = true; };
          "11-bond" = { netdevConfig = { Name = "bond0"; Kind = "bond"; }; bondConfig = { Mode = "802.3ad"; MIIMonitorSec = "100ms"; LACPTransmitRate = "fast"; }; };
        };
        networks = {
          "10-lan" = { matchConfig.Name = "lan"; address = [ "192.0.2.1/24" ]; networkConfig.ConfigureWithoutCarrier = true; };
          "11-direct" = { matchConfig.Name = "eth1"; networkConfig.Bridge = "lan"; };
          "12-bond" = { matchConfig.Name = "bond0"; networkConfig.Bridge = "lan"; };
          "13-members" = { matchConfig.Name = "eth2 eth3"; networkConfig.Bond = "bond0"; };
        };
      };
      services.dnsmasq = {
        enable = true;
        settings = { interface = "lan"; bind-interfaces = true; dhcp-range = "192.0.2.50,192.0.2.100,255.255.255.0,1m"; dhcp-option = [ "option:router,192.0.2.1" "option:dns-server,192.0.2.1" ]; };
      };
    };
  };
  testScript = ''
    import json, shlex, time
    start_all()
    peer.wait_for_unit("dnsmasq.service")
    host.wait_for_unit("floatlab-hostd.service")
    helper = """import json,socket,sys
    s=socket.socket(socket.AF_UNIX);s.connect('/run/floatlab/hostd.sock')
    s.sendall((json.dumps({'id':'test','type':'command','payload':{'name':sys.argv[1],'payload':json.loads(sys.argv[2])}})+'\\n').encode())
    f=s.makefile()
    while True:
     r=json.loads(f.readline())
     if r.get('id')=='test' and r.get('type')=='response':
      result=r['payload'];assert result['ok'],result;print(json.dumps(result['payload']));break
    """
    host.succeed("cat > /tmp/network-ipc.py <<'PY'\n" + helper + "\nPY")
    host.wait_until_succeeds("python3 /tmp/network-ipc.py net.settings.get null")
    def rpc(name, payload=None):
        return json.loads(host.succeed("python3 /tmp/network-ipc.py " + shlex.quote(name) + " " + shlex.quote(json.dumps(payload))))
    def apply(config, confirm=True):
        status=rpc("net.settings.get")
        change=rpc("net.settings.apply", {"revision":status["revision"],"config":config,"key":str(time.time_ns()),"actor":"test"})
        for _ in range(500):
            change=rpc("net.settings.change", {"id":change["id"]})
            if change["state"] != "applying": break
            time.sleep(0.1)
        assert change["state"] == "awaiting_confirmation", change
        if confirm: rpc("net.settings.confirm", {"id":change["id"]})
        return change
    status=rpc("net.settings.get")
    assert status["config"]["ipv4"]["mode"] == "dhcp"
    adapters={a["name"]:a["permanent_mac"] for a in status["live"]["adapters"] if a["eligible"]}
    config=status["config"]
    # Isolate the tested LAN from QEMU's management DHCP server.
    config["excluded_macs"]=[adapters["eth0"]]
    apply(config)
    host.wait_until_succeeds("ip -4 addr show floatlab-lan | grep '192.0.2.'")
    host.succeed("ping -c 2 192.0.2.1")
    mac=rpc("net.settings.get")["live"]["bridge_mac"]
    service={"stack_id":"test-stack","pool_id":"test-pool","address":"192.0.2.120/24"}
    rpc("net.service.add",service)
    host.succeed("networkctl reload; networkctl reconfigure floatlab-lan")
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    host.succeed("networkctl renew floatlab-lan")
    host.wait_until_succeeds("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    config["ipv4"]={"mode":"static","address":"192.0.2.2","netmask":"255.255.255.0","gateway":"192.0.2.1"}
    apply(config)
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.2/24'")
    host.succeed("resolvectl dns floatlab-lan | grep '1.1.1.1.*8.8.8.8'")
    config["excluded_macs"]=[adapters["eth0"],adapters["eth1"]]
    config["bonds"]=[{"name":"bond0","mode":"802.3ad","members":[adapters["eth2"],adapters["eth3"]]}]
    apply(config)
    host.wait_until_succeeds("grep -q 'Number of ports: 2' /proc/net/bonding/bond0")
    peer.wait_until_succeeds("grep -q 'Number of ports: 2' /proc/net/bonding/bond0")
    # The direct uplink is excluded, so these packets must cross the LACP bond.
    host.wait_until_succeeds("ping -c 2 192.0.2.1")
    assert rpc("net.settings.get")["live"]["bridge_mac"] == mac
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    config["bonds"]=[]
    config["excluded_macs"]=[adapters["eth0"]]
    change=apply(config,False)
    host.fail("ip link show bond0")
    # The timer must restore networking with the host daemon stopped.
    host.succeed("systemctl stop floatlab-hostd")
    host.wait_until_succeeds("test -e /proc/net/bonding/bond0",timeout=80)
    host.succeed("systemctl start floatlab-hostd")
    host.wait_until_succeeds("python3 /tmp/network-ipc.py net.settings.get null")
    assert rpc("net.settings.change",{"id":change["id"]})["state"] == "rolled_back"
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    config["bonds"]=[]
    apply(config)
    # Restart the VM with its existing disk; the driver uses QEMU -no-reboot.
    host.shutdown()
    host.start()
    host.wait_for_unit("floatlab-hostd.service")
    host.succeed("cat > /tmp/network-ipc.py <<'PY'\n" + helper + "\nPY")
    host.wait_until_succeeds("python3 /tmp/network-ipc.py net.settings.get null")
    assert rpc("net.settings.get")["config"]["ipv4"]["mode"] == "static"
    # Reboot restoration is authorized by cluster allocation ownership.
    rpc("net.service.sync",[service])
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    config["ipv4"]={"mode":"dhcp"}
    config["dns_servers"]=[]
    apply(config)
    host.wait_until_succeeds("resolvectl dns floatlab-lan | grep '192.0.2.1'")
    host.succeed("ip -4 addr show floatlab-lan | grep '192.0.2.120/24'")
    # Remove server access and allow a full lease to expire, retaining aliases.
    peer.succeed("systemctl stop dnsmasq")
    for _ in range(180):
        addresses=rpc("net.settings.get")["live"]["addresses"]
        if addresses == ["192.0.2.120/24"]: break
        time.sleep(1)
    assert "192.0.2.120/24" in addresses, addresses
    assert not any(a.startswith("192.0.2.") and a!="192.0.2.120/24" for a in addresses), addresses
  '';
}
