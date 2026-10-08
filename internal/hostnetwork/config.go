package hostnetwork

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"regexp"
	"strings"

	"github.com/floatlab/floatlab-core/pkg/ipc"
)

const Bridge = "floatlab-lan"
const ConfigPath = "/floatlab/system/etc/network.toml"

type IPv4 struct {
	Mode    string `json:"mode" toml:"mode"`
	Address string `json:"address,omitempty" toml:"address,omitempty"`
	Netmask string `json:"netmask,omitempty" toml:"netmask,omitempty"`
	Gateway string `json:"gateway,omitempty" toml:"gateway,omitempty"`
}
type Bond struct {
	Name    string   `json:"name" toml:"name"`
	Mode    string   `json:"mode" toml:"mode"`
	Members []string `json:"members" toml:"members"`
}
type Config struct {
	Version       int      `json:"version" toml:"version"`
	ExcludedMACs  []string `json:"excluded_macs" toml:"excluded_macs"`
	Bonds         []Bond   `json:"bonds" toml:"bonds"`
	IPv4          IPv4     `json:"ipv4" toml:"ipv4"`
	DNSServers    []string `json:"dns_servers" toml:"dns_servers"`
	DefaultPoolID string   `json:"default_pool_id,omitempty" toml:"default_pool_id,omitempty"`
}

func DefaultConfig() Config {
	return Config{Version: 1, IPv4: IPv4{Mode: "dhcp"}, DNSServers: []string{"1.1.1.1", "8.8.8.8"}, ExcludedMACs: []string{}, Bonds: []Bond{}}
}
func Error(code, message string) error {
	return &ipc.RPCError{Code: "network." + code, Message: message}
}
func Revision(c Config) string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func MAC(value string) (string, error) {
	mac, err := net.ParseMAC(value)
	if err != nil || len(mac) != 6 || mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
		return "", fmt.Errorf("invalid unicast Ethernet MAC %q", value)
	}
	return mac.String(), nil
}
func (v IPv4) Prefix() (netip.Prefix, error) {
	ip, err := netip.ParseAddr(v.Address)
	if err != nil || !ip.Is4() || !ip.IsGlobalUnicast() {
		return netip.Prefix{}, fmt.Errorf("invalid static IPv4 address")
	}
	mask := net.ParseIP(v.Netmask).To4()
	if mask == nil {
		return netip.Prefix{}, fmt.Errorf("invalid IPv4 netmask")
	}
	ones, bits := net.IPMask(mask).Size()
	if bits != 32 || ones == 0 || ones > 30 {
		return netip.Prefix{}, fmt.Errorf("netmask must be contiguous /1 through /30")
	}
	prefix := netip.PrefixFrom(ip, ones)
	network := prefix.Masked().Addr().As4()
	a := ip.As4()
	value := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	base := uint32(network[0])<<24 | uint32(network[1])<<16 | uint32(network[2])<<8 | uint32(network[3])
	if value == base || value == base|uint32((uint64(1)<<uint(32-ones))-1) {
		return netip.Prefix{}, fmt.Errorf("static address cannot be network or broadcast")
	}
	return prefix, nil
}

var bondName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,14}$`)

func (c *Config) Validate() error {
	if c.ExcludedMACs == nil {
		c.ExcludedMACs = []string{}
	}
	if c.Bonds == nil {
		c.Bonds = []Bond{}
	}
	if c.DNSServers == nil {
		c.DNSServers = []string{}
	}
	invalid := func(err error) error { return Error("invalid", err.Error()) }
	if c.Version != 1 {
		return Error("invalid", "unsupported network configuration version")
	}
	used := map[string]bool{}
	for i, value := range c.ExcludedMACs {
		mac, err := MAC(value)
		if err != nil {
			return invalid(err)
		}
		if used[mac] {
			return Error("invalid", "duplicate adapter MAC")
		}
		used[mac] = true
		c.ExcludedMACs[i] = mac
	}
	names := map[string]bool{}
	for i := range c.Bonds {
		b := &c.Bonds[i]
		if !bondName.MatchString(b.Name) || b.Name == Bridge || names[b.Name] || b.Mode != "802.3ad" || len(b.Members) < 2 {
			return Error("invalid", "bond requires a unique interface name, 802.3ad mode and at least two members")
		}
		names[b.Name] = true
		for j, value := range b.Members {
			mac, err := MAC(value)
			if err != nil {
				return invalid(err)
			}
			if used[mac] {
				return Error("invalid", "bond members must be unique and cannot be excluded")
			}
			used[mac] = true
			b.Members[j] = mac
		}
	}
	switch c.IPv4.Mode {
	case "dhcp":
		if c.IPv4.Address != "" || c.IPv4.Netmask != "" || c.IPv4.Gateway != "" {
			return Error("invalid", "DHCP cannot specify static addressing")
		}
	case "static":
		prefix, err := c.IPv4.Prefix()
		if err != nil {
			return invalid(err)
		}
		gw, err := netip.ParseAddr(c.IPv4.Gateway)
		if err != nil || !gw.Is4() || !gw.IsGlobalUnicast() || !prefix.Contains(gw) || gw == prefix.Addr() || gw == prefix.Masked().Addr() || isBroadcast(gw, prefix.Bits()) {
			return Error("invalid", "gateway must be a different usable address in the static subnet")
		}
	default:
		return Error("invalid", "ipv4.mode must be dhcp or static")
	}
	for _, value := range c.DNSServers {
		ip, err := netip.ParseAddr(value)
		if err != nil || !ip.IsGlobalUnicast() {
			return Error("invalid", "invalid DNS server")
		}
	}
	if strings.ContainsAny(c.DefaultPoolID, "\n\r\x00") {
		return Error("invalid", "invalid default pool ID")
	}
	return nil
}

type Adapter struct {
	Name         string `json:"name"`
	MAC          string `json:"mac"`
	PermanentMAC string `json:"permanent_mac"`
	Eligible     bool   `json:"eligible"`
	Master       string `json:"master,omitempty"`
	Up           bool   `json:"up"`
}
type Live struct {
	PrimaryAddresses []string  `json:"primary_addresses"`
	Adapters         []Adapter `json:"adapters"`
	Addresses        []string  `json:"addresses"`
	Gateways         []string  `json:"gateways"`
	DNSServers       []string  `json:"dns_servers"`
	BridgeMAC        string    `json:"bridge_mac"`
}
type Service struct {
	StackID string `json:"stack_id"`
	Address string `json:"address"`
	PoolID  string `json:"pool_id"`
}
type Update struct {
	Revision string `json:"revision"`
	Config   Config `json:"config"`
	Key      string `json:"-"`
	Actor    string `json:"-"`
}

// ApplyPayload carries the HTTP idempotency identity over the local IPC channel.
type ApplyPayload struct {
	Revision string `json:"revision"`
	Config   Config `json:"config"`
	Key      string `json:"key"`
	Actor    string `json:"actor"`
}

func isBroadcast(ip netip.Addr, bits int) bool {
	a := ip.As4()
	value := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
	hostmask := uint32((uint64(1) << uint(32-bits)) - 1)
	return value&hostmask == hostmask
}
