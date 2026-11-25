package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/internal/qemu"
)

const (
	networkWait = 3 * time.Minute
	aliasCheck  = "ip -4 addr show floatlab-lan | grep -q '192.0.2.120/24'"
	hostdReady  = "systemctl is-active --quiet floatlab-hostd.service && hostd-test-rpc net.settings.get null"
)

func (v *vm) rpc(ctx context.Context, command string, payload, result any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	argument := "'" + strings.ReplaceAll(string(data), "'", "'\\''") + "'"
	out, err := v.run(ctx, "hostd-test-rpc "+command+" "+argument)
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(out), result); err != nil {
		return fmt.Errorf("%s response: %w", command, err)
	}
	return nil
}

func (v *vm) status(ctx context.Context) (hostnetwork.Status, error) {
	var status hostnetwork.Status
	err := v.rpc(ctx, "net.settings.get", nil, &status)
	return status, err
}

func (v *vm) apply(ctx context.Context, config hostnetwork.Config, confirm bool) (string, error) {
	status, err := v.status(ctx)
	if err != nil {
		return "", err
	}
	request := hostnetwork.ApplyPayload{
		Revision: status.Revision, Config: config, Key: strconv.FormatInt(time.Now().UnixNano(), 10), Actor: "test",
	}
	var change hostnetwork.Change
	if err := v.rpc(ctx, "net.settings.apply", request, &change); err != nil {
		return "", err
	}
	if change.ID == "" {
		return "", errors.New("network change ID is missing")
	}
	id := map[string]string{"id": change.ID}
	if err := retry(ctx, 50*time.Second, 100*time.Millisecond, func(ctx context.Context) error {
		if err := v.rpc(ctx, "net.settings.change", id, &change); err != nil {
			return err
		}
		if change.State == "applying" {
			return errors.New("network change still applying")
		}
		return nil
	}); err != nil {
		return "", err
	}
	if change.State != "awaiting_confirmation" {
		return "", fmt.Errorf("change %s should await confirmation, got %s (%s)", change.ID, change.State, change.Error)
	}
	if confirm {
		if err := v.rpc(ctx, "net.settings.confirm", id, nil); err != nil {
			return "", err
		}
	}
	return change.ID, nil
}

func testNetwork(ctx context.Context, host, peer *vm) error {
	if err := peer.boot(ctx); err != nil {
		return err
	}
	if err := host.boot(ctx); err != nil {
		return err
	}
	if err := peer.wait(ctx, networkWait, "systemctl is-active --quiet dnsmasq.service"); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, hostdReady); err != nil {
		return err
	}
	status, err := host.status(ctx)
	if err != nil {
		return err
	}
	config := status.Config
	if config.IPv4.Mode != "dhcp" {
		return fmt.Errorf("initial network mode = %q, want dhcp", config.IPv4.Mode)
	}
	adapters := make(map[string]string, 4)
	for _, a := range status.Live.Adapters {
		if a.Eligible {
			adapters[a.Name] = a.PermanentMAC
		}
	}
	for _, name := range []string{"test0", "test1", "test2", "test3"} {
		if adapters[name] == "" {
			return fmt.Errorf("missing eligible adapter %s: %+v", name, status.Live.Adapters)
		}
	}

	// Isolate the tested LAN from QEMU's management DHCP server.
	config.ExcludedMACs = []string{adapters["test0"]}
	if _, err := host.apply(ctx, config, true); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, "ip -4 addr show floatlab-lan | grep -q '192.0.2.'"); err != nil {
		return err
	}
	if _, err := host.run(ctx, "ping -c 2 192.0.2.1"); err != nil {
		return err
	}
	status, err = host.status(ctx)
	if err != nil {
		return err
	}
	bridgeMAC := status.Live.BridgeMAC
	service := hostnetwork.Service{StackID: "test-stack", PoolID: "test-pool", Address: "192.0.2.120/24"}
	if err := host.rpc(ctx, "net.service.add", service, nil); err != nil {
		return err
	}
	if _, err := host.run(ctx, "networkctl reload; networkctl reconfigure floatlab-lan; "+aliasCheck+"; networkctl renew floatlab-lan"); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, aliasCheck); err != nil {
		return err
	}
	fmt.Println("Verified DHCP and service address renewal")

	config.IPv4 = hostnetwork.IPv4{Mode: "static", Address: "192.0.2.2", Netmask: "255.255.255.0", Gateway: "192.0.2.1"}
	if _, err := host.apply(ctx, config, true); err != nil {
		return err
	}
	if _, err := host.run(ctx, "ip -4 addr show floatlab-lan | grep -q '192.0.2.2/24'; resolvectl dns floatlab-lan | grep -q '1.1.1.1.*8.8.8.8'"); err != nil {
		return err
	}
	config.ExcludedMACs = []string{adapters["test0"], adapters["test1"]}
	config.Bonds = []hostnetwork.Bond{{Name: "bond0", Mode: "802.3ad", Members: []string{adapters["test2"], adapters["test3"]}}}
	if _, err := host.apply(ctx, config, true); err != nil {
		return err
	}
	for _, machine := range []*vm{host, peer} {
		if err := machine.wait(ctx, networkWait, "grep -q 'Number of ports: 2' /proc/net/bonding/bond0"); err != nil {
			return err
		}
	}
	// With the direct uplink excluded, these packets must cross the LACP bond.
	if err := host.wait(ctx, networkWait, "ping -c 2 192.0.2.1"); err != nil {
		return err
	}
	status, err = host.status(ctx)
	if err != nil {
		return err
	}
	if bridgeMAC == "" || status.Live.BridgeMAC != bridgeMAC {
		return fmt.Errorf("bridge MAC changed from %q to %q", bridgeMAC, status.Live.BridgeMAC)
	}
	if _, err := host.run(ctx, aliasCheck); err != nil {
		return err
	}
	fmt.Println("Verified static IPv4, DNS, LACP forwarding, and bridge MAC stability")

	config.Bonds = []hostnetwork.Bond{}
	config.ExcludedMACs = []string{adapters["test0"]}
	changeID, err := host.apply(ctx, config, false)
	if err != nil {
		return err
	}
	_, err = host.run(ctx, "ip link show bond0")
	var exit *qemu.GuestExitError
	if !errors.As(err, &exit) || exit.Code != 1 || exit.Signal != 0 {
		return fmt.Errorf("expected bond0 to be removed, got %v", err)
	}
	// The rollback timer must restore networking while the daemon is stopped.
	if _, err := host.run(ctx, "systemctl stop floatlab-hostd"); err != nil {
		return err
	}
	if err := host.wait(ctx, 80*time.Second, "test -e /proc/net/bonding/bond0"); err != nil {
		return err
	}
	if _, err := host.run(ctx, "systemctl start floatlab-hostd"); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, hostdReady); err != nil {
		return err
	}
	var change hostnetwork.Change
	if err := host.rpc(ctx, "net.settings.change", map[string]string{"id": changeID}, &change); err != nil {
		return err
	}
	if change.State != "rolled_back" {
		return fmt.Errorf("change %s state = %q, want rolled_back", changeID, change.State)
	}
	if _, err := host.run(ctx, aliasCheck); err != nil {
		return err
	}
	if _, err := host.apply(ctx, config, true); err != nil {
		return err
	}
	if err := host.reboot(ctx); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, hostdReady); err != nil {
		return err
	}
	status, err = host.status(ctx)
	if err != nil {
		return err
	}
	if status.Config.IPv4.Mode != "static" {
		return fmt.Errorf("network mode after reboot = %q, want static", status.Config.IPv4.Mode)
	}
	// Reboot restoration is authorized by cluster allocation ownership.
	if err := host.rpc(ctx, "net.service.sync", []hostnetwork.Service{service}, nil); err != nil {
		return err
	}
	if _, err := host.run(ctx, aliasCheck); err != nil {
		return err
	}
	fmt.Println("Verified watchdog rollback and restoration after reboot")

	config.IPv4 = hostnetwork.IPv4{Mode: "dhcp"}
	config.DNSServers = []string{}
	if _, err := host.apply(ctx, config, true); err != nil {
		return err
	}
	if err := host.wait(ctx, networkWait, "resolvectl dns floatlab-lan | grep -q '192.0.2.1'"); err != nil {
		return err
	}
	if _, err := host.run(ctx, aliasCheck); err != nil {
		return err
	}
	// Remove server access and allow the lease to expire while retaining the alias.
	if _, err := peer.run(ctx, "systemctl stop dnsmasq"); err != nil {
		return err
	}
	if err := retry(ctx, 3*time.Minute, time.Second, func(ctx context.Context) error {
		status, err := host.status(ctx)
		if err != nil {
			return err
		}
		addresses := status.Live.Addresses
		if !slices.Contains(addresses, service.Address) || slices.ContainsFunc(addresses, func(address string) bool {
			return strings.HasPrefix(address, "192.0.2.") && address != service.Address
		}) {
			return fmt.Errorf("expected only the service address on the tested subnet after lease expiry, got %v", addresses)
		}
		return nil
	}); err != nil {
		return err
	}
	fmt.Println("PASS: host networking and DHCP lease expiry")
	return nil
}
