//go:build linux

package ipam

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// AddRoute installs an IPv6 host route for the given CIDR prefix via the nexthop.
// prefix is a CIDR string (e.g., "fd00::3/128"), nexthop is an IPv6 address.
func AddRoute(prefix, nexthop string) error {
	_, dst, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("ipam: parse route prefix %q: %w", prefix, err)
	}
	gw := net.ParseIP(nexthop)
	if gw == nil {
		return fmt.Errorf("ipam: parse nexthop %q: invalid IP", nexthop)
	}
	route := &netlink.Route{Dst: dst, Gw: gw}
	if err := netlink.RouteAdd(route); err != nil {
		return fmt.Errorf("ipam: add route %s via %s: %w", prefix, nexthop, err)
	}
	return nil
}

// DelRoute removes the IPv6 host route for the given prefix.
func DelRoute(prefix, nexthop string) error {
	_, dst, err := net.ParseCIDR(prefix)
	if err != nil {
		return fmt.Errorf("ipam: parse route prefix %q: %w", prefix, err)
	}
	gw := net.ParseIP(nexthop)
	if gw == nil {
		return fmt.Errorf("ipam: parse nexthop %q: invalid IP", nexthop)
	}
	route := &netlink.Route{Dst: dst, Gw: gw}
	if err := netlink.RouteDel(route); err != nil {
		return fmt.Errorf("ipam: del route %s via %s: %w", prefix, nexthop, err)
	}
	return nil
}
