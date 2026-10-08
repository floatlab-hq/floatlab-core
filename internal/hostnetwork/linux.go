//go:build linux

package hostnetwork

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const owner = "floatlab:network"
const networkUnit = "/run/systemd/network/10-floatlab-lan.network"

type Linux struct{}

func command(ctx context.Context, name string, args ...string) error {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(output)))
	}
	return nil
}
func discover() ([]netlink.Link, []Adapter, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, nil, err
	}
	adapters := []Adapter{}
	for _, l := range links {
		a := l.Attrs()
		mac := a.PermHWAddr.String()
		if _, err := MAC(mac); err != nil {
			mac = a.HardwareAddr.String()
			if output, err := permanentAddress(l); err == nil {
				if _, value, ok := strings.Cut(strings.TrimSpace(string(output)), ": "); ok {
					if permanent, err := MAC(value); err == nil {
						mac = permanent
					}
				}
			}
		}
		_, wireless := os.Stat(filepath.Join("/sys/class/net", a.Name, "wireless"))
		_, device := os.Stat(filepath.Join("/sys/class/net", a.Name, "device"))
		eligible := eligibleAdapter(l.Type(), device == nil, wireless == nil, a.Flags)
		if _, err := MAC(mac); err != nil {
			eligible = false
		}
		master := ""
		if a.MasterIndex != 0 {
			if v, err := netlink.LinkByIndex(a.MasterIndex); err == nil {
				master = v.Attrs().Name
			}
		}
		adapters = append(adapters, Adapter{Name: a.Name, MAC: a.HardwareAddr.String(), PermanentMAC: mac, Eligible: eligible, Master: master, Up: a.Flags&net.FlagUp != 0})
	}
	sort.Slice(adapters, func(i, j int) bool { return adapters[i].Name < adapters[j].Name })
	return links, adapters, nil
}
func (Linux) Live(ctx context.Context) (Live, error) {
	_, adapters, err := discover()
	live := Live{Adapters: adapters, Addresses: []string{}, Gateways: []string{}, DNSServers: []string{}}
	if err != nil {
		return live, err
	}
	bridge, err := netlink.LinkByName(Bridge)
	if err != nil {
		var missing netlink.LinkNotFoundError
		if errors.As(err, &missing) {
			return live, nil
		}
		return live, err
	}
	live.BridgeMAC = bridge.Attrs().HardwareAddr.String()
	addresses, err := netlink.AddrList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return live, err
	}
	for _, a := range addresses {
		live.Addresses = append(live.Addresses, a.IPNet.String())
	}
	routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return live, err
	}
	for _, r := range routes {
		if r.Gw != nil && (r.Dst == nil || r.Dst.String() == "0.0.0.0/0") {
			live.Gateways = append(live.Gateways, r.Gw.String())
		}
	}
	output, err := exec.CommandContext(ctx, "resolvectl", "dns", Bridge).Output()
	if err == nil {
		if _, values, ok := strings.Cut(strings.TrimSpace(string(output)), ":"); ok {
			live.DNSServers = strings.Fields(values)
		}
	}
	return live, nil
}
func configureDHCP(c Config) string {
	dhcp := "no"
	if c.IPv4.Mode == "dhcp" {
		dhcp = "ipv4"
	}
	dns := "yes"
	if len(c.DNSServers) > 0 {
		dns = "no"
	}
	return fmt.Sprintf("[Match]\nName=%s\n\n[Link]\nRequiredForOnline=no\n\n[Network]\nDHCP=%s\nKeepConfiguration=static\nConfigureWithoutCarrier=yes\nLinkLocalAddressing=no\nIPv6AcceptRA=no\n\n[DHCPv4]\nClientIdentifier=mac\nUseDNS=%s\nUseNTP=no\nUseHostname=no\nUseMTU=no\n", Bridge, dhcp, dns)
}
func removeDHCP(bridge netlink.Link, sources []net.IP) error {
	addresses, err := netlink.AddrList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, a := range addresses {
		leased := a.Flags&unix.IFA_F_PERMANENT == 0
		for _, source := range sources {
			if a.IP.Equal(source) {
				leased = true
			}
		}
		if leased {
			if err = netlink.AddrDel(bridge, &a); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
				return err
			}
		}
	}
	routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return err
	}
	for _, r := range routes {
		if r.Protocol == unix.RTPROT_DHCP {
			if err = netlink.RouteDel(&r); err != nil && !errors.Is(err, unix.ESRCH) {
				return err
			}
		}
	}
	return nil
}
func (Linux) Apply(ctx context.Context, previous, c Config, bridgeMAC string, services map[string]Service) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	links, adapters, err := discover()
	if err != nil {
		return err
	}
	byMAC := map[string]netlink.Link{}
	byName := map[string]netlink.Link{}
	for _, l := range links {
		byName[l.Attrs().Name] = l
	}
	for _, a := range adapters {
		if a.Eligible {
			byMAC[a.PermanentMAC] = byName[a.Name]
		}
	}
	bridge := byName[Bridge]
	if bridge == nil {
		mac, err := net.ParseMAC(bridgeMAC)
		if err != nil {
			return err
		}
		if err = prepareLink(ctx, Bridge, "bridge"); err != nil {
			return err
		}
		bridge = &netlink.Bridge{LinkAttrs: netlink.LinkAttrs{Name: Bridge, Alias: owner, HardwareAddr: mac}}
		if err = netlink.LinkAdd(bridge); err != nil {
			return err
		}
		// Finish udev processing before configuring membership and addresses.
		if err = command(ctx, "udevadm", "settle"); err != nil {
			return err
		}
		bridge, err = netlink.LinkByName(Bridge)
		if err != nil {
			return err
		}
		if err = netlink.LinkSetAlias(bridge, owner); err != nil {
			return err
		}
	} else if bridge.Type() != "bridge" || bridge.Attrs().Alias != owner {
		return fmt.Errorf("LAN bridge name is occupied by an unmanaged interface")
	}
	// Multiple bridged uplinks can form a loop; enable kernel STP before attaching them.
	stpPath := filepath.Join("/sys/class/net", Bridge, "bridge/stp_state")
	stp, err := os.ReadFile(stpPath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(stp)) != "1" {
		if err = os.WriteFile(stpPath, []byte("1\n"), 0600); err != nil {
			return err
		}
	}
	// Stop lease management before removing DHCP state; otherwise leave renewals alone.
	topologyChanged := !reflect.DeepEqual(previous.ExcludedMACs, c.ExcludedMACs) || !reflect.DeepEqual(previous.Bonds, c.Bonds)
	for _, a := range adapters {
		if !a.Eligible {
			continue
		}
		wantedMaster := ""
		if !contains(c.ExcludedMACs, a.PermanentMAC) {
			wantedMaster = Bridge
		}
		for _, b := range c.Bonds {
			if contains(b.Members, a.PermanentMAC) {
				wantedMaster = b.Name
			}
		}
		if a.Master != wantedMaster {
			topologyChanged = true
		}
	}
	if c.IPv4.Mode == "static" || topologyChanged {
		sources, err := dhcpSources(bridge)
		if err != nil {
			return err
		}
		paused := c
		paused.IPv4 = IPv4{Mode: "static"}
		if err = writeNetworkUnit(ctx, paused); err != nil {
			return err
		}
		if err = removeDHCP(bridge, sources); err != nil {
			return err
		}
	}
	wanted := map[string]Bond{}
	for _, b := range c.Bonds {
		wanted[b.Name] = b
	}
	// Detach only physical adapters whose master must change. Never detach service veths.
	target := map[string]string{}
	for mac := range byMAC {
		if !contains(c.ExcludedMACs, mac) {
			target[mac] = Bridge
		}
	}
	for _, b := range c.Bonds {
		for _, mac := range b.Members {
			if byMAC[mac] != nil {
				target[mac] = b.Name
			}
		}
	}
	for mac, l := range byMAC {
		master := ""
		if old := l.Attrs().MasterIndex; old != 0 {
			if ml, err := netlink.LinkByIndex(old); err == nil {
				master = ml.Attrs().Name
			}
		}
		if master != target[mac] && l.Attrs().MasterIndex != 0 {
			if master != Bridge {
				ml, err := netlink.LinkByIndex(l.Attrs().MasterIndex)
				if err != nil {
					return err
				}
				if ml.Attrs().Alias != owner {
					return fmt.Errorf("adapter %s belongs to unmanaged master %s", l.Attrs().Name, master)
				}
			}
			if err = netlink.LinkSetNoMaster(l); err != nil {
				return err
			}
		}
	}
	for _, l := range links {
		if l.Type() == "bond" && l.Attrs().Alias == owner {
			if _, ok := wanted[l.Attrs().Name]; !ok {
				if err = netlink.LinkDel(l); err != nil {
					return err
				}
			}
		}
	}
	for _, b := range c.Bonds {
		bond, err := netlink.LinkByName(b.Name)
		if err != nil {
			var missing netlink.LinkNotFoundError
			if !errors.As(err, &missing) {
				return err
			}
			if err = prepareLink(ctx, b.Name, "bond"); err != nil {
				return err
			}
			v := netlink.NewLinkBond(netlink.LinkAttrs{Name: b.Name, Alias: owner})
			v.Mode = netlink.BOND_MODE_802_3AD
			v.Miimon = 100
			v.LacpRate = netlink.BOND_LACP_RATE_FAST
			v.XmitHashPolicy = netlink.BOND_XMIT_HASH_POLICY_LAYER2_3
			if err = netlink.LinkAdd(v); err != nil {
				return err
			}
			if err = command(ctx, "udevadm", "settle"); err != nil {
				return err
			}
			bond, err = netlink.LinkByName(b.Name)
			if err != nil {
				return err
			}
			if err = netlink.LinkSetAlias(bond, owner); err != nil {
				return err
			}
		} else {
			v, ok := bond.(*netlink.Bond)
			if !ok || v.Alias != owner || v.Mode != netlink.BOND_MODE_802_3AD {
				return fmt.Errorf("bond %s conflicts with existing interface", b.Name)
			}
		}
		for _, mac := range b.Members {
			l := byMAC[mac]
			if l == nil {
				continue
			}
			if l.Attrs().MasterIndex != bond.Attrs().Index {
				if err = netlink.LinkSetDown(l); err != nil {
					return err
				}
				if err = netlink.LinkSetMaster(l, bond); err != nil {
					return err
				}
			}
			if err = netlink.LinkSetUp(l); err != nil {
				return err
			}
		}
		if err = netlink.LinkSetMaster(bond, bridge); err != nil {
			return err
		}
		if err = netlink.LinkSetUp(bond); err != nil {
			return err
		}
	}
	for mac, l := range byMAC {
		if target[mac] == Bridge {
			if err = netlink.LinkSetMaster(l, bridge); err != nil {
				return err
			}
			if err = netlink.LinkSetUp(l); err != nil {
				return err
			}
		}
	}
	mac, err := net.ParseMAC(bridgeMAC)
	if err != nil {
		return err
	}
	if err = netlink.LinkSetHardwareAddr(bridge, mac); err != nil {
		return err
	}
	if err = netlink.LinkSetUp(bridge); err != nil {
		return err
	}
	if previous.IPv4.Mode == "static" && previous.IPv4 != c.IPv4 {
		p, err := previous.IPv4.Prefix()
		if err != nil {
			return err
		}
		addr, _ := netlink.ParseAddr(p.String())
		if err = netlink.AddrDel(bridge, addr); err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
			return err
		}
		route := netlink.Route{LinkIndex: bridge.Attrs().Index, Gw: net.ParseIP(previous.IPv4.Gateway), Protocol: unix.RTPROT_STATIC}
		if err = netlink.RouteDel(&route); err != nil && !errors.Is(err, unix.ESRCH) {
			return err
		}
	}
	if c.IPv4.Mode == "static" {
		p, err := c.IPv4.Prefix()
		if err != nil {
			return err
		}
		addr, _ := netlink.ParseAddr(p.String())
		if err = netlink.AddrReplace(bridge, addr); err != nil {
			return err
		}
		route := netlink.Route{LinkIndex: bridge.Attrs().Index, Gw: net.ParseIP(c.IPv4.Gateway), Protocol: unix.RTPROT_STATIC}
		if err = netlink.RouteReplace(&route); err != nil {
			return err
		}
	}
	for _, service := range services {
		if err = (Linux{}).Service(ctx, service, true); err != nil {
			return err
		}
	}
	if err = writeNetworkUnit(ctx, c); err != nil {
		return err
	}
	if len(c.DNSServers) > 0 {
		args := append([]string{"dns", Bridge}, c.DNSServers...)
		if err = command(ctx, "resolvectl", args...); err != nil {
			return err
		}
		return command(ctx, "resolvectl", "domain", Bridge, "~.")
	}
	// Drop hostd's DNS override and let networkd publish DHCP DNS.
	if err = command(ctx, "resolvectl", "revert", Bridge); err != nil {
		return err
	}
	return command(ctx, "networkctl", "reconfigure", Bridge)
}
func (Linux) Arm(ctx context.Context, id string, deadline time.Time) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	return command(ctx, "systemd-run", "--quiet", "--collect", "--unit=floatlab-network-rollback-"+id, "--on-calendar="+deadline.UTC().Format("2006-01-02 15:04:05.000000 MST"), "--timer-property=AccuracySec=1us", "--property=TimeoutStartSec=60s", "--property=Restart=on-failure", "--property=RestartSec=2s", "--setenv=PATH="+os.Getenv("PATH"), binary, "network", "rollback", id, "--expired")
}
func (Linux) Cancel(ctx context.Context, id string) error {
	return command(ctx, "systemctl", "stop", "floatlab-network-rollback-"+id+".timer")
}
func (Linux) Service(ctx context.Context, s Service, add bool) error {
	if _, err := parseService(s); err != nil {
		return err
	}
	bridge, err := netlink.LinkByName(Bridge)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(s.Address)
	if err != nil {
		return err
	}
	if !add {
		err = netlink.AddrDel(bridge, addr)
		if errors.Is(err, unix.EADDRNOTAVAIL) {
			return nil
		}
		return err
	}
	// Move legacy stack addresses off the owned veth before advertising the bridge alias.
	links, err := netlink.LinkList()
	if err != nil {
		return err
	}
	for _, link := range links {
		if link.Attrs().Alias == "floatlab:"+s.StackID {
			addresses, err := netlink.AddrList(link, netlink.FAMILY_V4)
			if err != nil {
				return err
			}
			for _, old := range addresses {
				if old.IP.Equal(addr.IP) {
					if err = netlink.AddrDel(link, &old); err != nil {
						return err
					}
				}
			}
		}
	}
	if err = netlink.AddrReplace(bridge, addr); err != nil {
		return err
	}
	return announce(bridge, addr.IP)
}
func announce(link netlink.Link, ip net.IP) error {
	// Ethernet gratuitous ARP request; the kernel continues to own ARP responses.
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(htons(unix.ETH_P_ARP)))
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	mac := link.Attrs().HardwareAddr
	if len(mac) != 6 || ip.To4() == nil {
		return fmt.Errorf("invalid ARP interface")
	}
	packet := make([]byte, 42)
	for i := 0; i < 6; i++ {
		packet[i] = 255
	}
	copy(packet[6:12], mac)
	copy(packet[12:22], []byte{8, 6, 0, 1, 8, 0, 6, 4, 0, 1})
	copy(packet[22:28], mac)
	copy(packet[28:32], ip.To4())
	copy(packet[38:42], ip.To4())
	return unix.Sendto(fd, packet, 0, &unix.SockaddrLinklayer{Ifindex: link.Attrs().Index, Protocol: htons(unix.ETH_P_ARP), Halen: 6, Addr: [8]uint8{255, 255, 255, 255, 255, 255}})
}
func htons(v uint16) uint16 { return v<<8 | v>>8 }
func (Linux) ArchiveLegacy() error {
	dir := "/floatlab/system/etc/systemd/network"
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	archive := filepath.Join(dir, "legacy-"+time.Now().UTC().Format("20060102T150405.000000000"))
	for _, entry := range entries {
		ext := filepath.Ext(entry.Name())
		if entry.IsDir() || (ext != ".network" && ext != ".netdev" && ext != ".link") {
			continue
		}
		if err = os.MkdirAll(archive, 0700); err != nil {
			return err
		}
		if err = os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(archive, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Udev must preserve ownership even if hostd dies during link creation. These
// .link policies only set metadata; networkd still creates no bridge or bond.
func prepareLink(ctx context.Context, name, kind string) error {
	path := filepath.Join(filepath.Dir(networkUnit), "05-floatlab-"+name+".link")
	data := []byte(fmt.Sprintf("[Match]\nOriginalName=%s\nKind=%s\n\n[Link]\nAlias=%s\nMACAddressPolicy=none\n", name, kind, owner))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := atomicWrite(path, data); err != nil {
		return err
	}
	if err := os.Chmod(path, 0644); err != nil {
		return err
	}
	return command(ctx, "udevadm", "control", "--reload")
}

func writeNetworkUnit(ctx context.Context, c Config) error {
	// Networkd runs as an unprivileged user and must read this generated unit.
	if err := os.MkdirAll(filepath.Dir(networkUnit), 0755); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(networkUnit), 0755); err != nil {
		return err
	}
	data := []byte(configureDHCP(c))
	old, err := os.ReadFile(networkUnit)
	if err == nil && string(old) == string(data) {
		if info, statErr := os.Stat(networkUnit); statErr == nil && info.Mode().Perm() == 0644 {
			return nil
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err = atomicWrite(networkUnit, data); err != nil {
		return err
	}
	if err = os.Chmod(networkUnit, 0644); err != nil {
		return err
	}
	if err = command(ctx, "networkctl", "reload"); err != nil {
		return err
	}
	return command(ctx, "networkctl", "reconfigure", Bridge)
}

func permanentAddress(link netlink.Link) ([]byte, error) {
	if link.Type() != "device" || link.Attrs().Flags&net.FlagLoopback != 0 {
		return nil, fmt.Errorf("not a physical adapter")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "ethtool", "-P", link.Attrs().Name).Output()
}
func eligibleAdapter(kind string, device, wireless bool, flags net.Flags) bool {
	return kind == "device" && device && !wireless && flags&net.FlagLoopback == 0
}

// DHCP routes record their preferred source even for infinite leases, whose
// addresses can otherwise look permanent to the kernel.
func dhcpSources(bridge netlink.Link) ([]net.IP, error) {
	routes, err := netlink.RouteList(bridge, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	sources := []net.IP{}
	for _, route := range routes {
		if route.Protocol == unix.RTPROT_DHCP && route.Src != nil {
			sources = append(sources, route.Src)
		}
	}
	return sources, nil
}
