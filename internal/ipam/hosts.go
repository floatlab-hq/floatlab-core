package ipam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func containsNode(nodes []string, node string) bool {
	for _, v := range nodes {
		if v == node {
			return true
		}
	}
	return false
}
func ValidateMembers(nodes []string) error {
	if len(nodes) == 0 {
		return fmt.Errorf("ipam: at least one member node is required")
	}
	seen := map[string]bool{}
	for _, node := range nodes {
		if node == "" || seen[node] {
			return fmt.Errorf("ipam: member nodes must be nonempty and unique")
		}
		seen[node] = true
	}
	return nil
}
func Member(pool Pool, node string) bool {
	return pool.MembershipResolved && containsNode(pool.NodeIDs, node)
}
func HostStatus(ctx context.Context, hosts *hostclient.Pool, node string) (hostnetwork.Status, error) {
	var status hostnetwork.Status
	raw, err := hosts.Execute(ctx, node, "net.settings.get", nil)
	if err != nil {
		return status, err
	}
	err = json.Unmarshal(raw, &status)
	return status, err
}
func Eligible(ctx context.Context, hosts *hostclient.Pool, pool Pool, node string) error {
	if !Member(pool, node) {
		return fmt.Errorf("ipam: node %s is not an eligible member of pool %s", node, pool.Name)
	}
	status, err := HostStatus(ctx, hosts, node)
	if err != nil {
		return err
	}
	if status.Change != nil {
		return fmt.Errorf("ipam: node has an outstanding network change")
	}
	if !hostnetwork.Compatible(pool.CIDR, status.Live.PrimaryAddresses) {
		return fmt.Errorf("ipam: pool %s does not match node %s LAN subnet", pool.Name, node)
	}
	return ValidateHostRange(pool, status.Live.PrimaryAddresses, status.Live.Gateways)
}
func ValidateHostRange(pool Pool, addresses, gateways []string) error {
	if !outsideRange(addresses, pool) {
		return fmt.Errorf("ipam: pool range includes a host LAN address")
	}
	for _, gateway := range gateways {
		if !outsideRange([]string{gateway + "/32"}, pool) {
			return fmt.Errorf("ipam: pool range includes a gateway")
		}
	}
	return nil
}

func ValidateHosts(ctx context.Context, db *rqlite.Client, hosts *hostclient.Pool, pool Pool) error {
	if err := ValidateMembers(pool.NodeIDs); err != nil {
		return err
	}
	pool.MembershipResolved = true
	for _, node := range pool.NodeIDs {
		if _, err := config.NewStore(db).GetNode(ctx, node); err != nil {
			return err
		}
		if err := Eligible(ctx, hosts, pool, node); err != nil {
			return err
		}
	}
	return nil
}

func outsideRange(addresses []string, pool Pool) bool {
	start, err := netip.ParseAddr(pool.StartIP)
	if err != nil {
		return false
	}
	end, err := netip.ParseAddr(pool.EndIP)
	if err != nil {
		return false
	}
	for _, value := range addresses {
		p, err := netip.ParsePrefix(value)
		if err != nil {
			return false
		}
		ip := p.Addr()
		if ip.Is4() && ip.Compare(start) >= 0 && ip.Compare(end) <= 0 {
			return false
		}
	}
	return true
}

func StackService(ctx context.Context, db *rqlite.Client, stackID string) (hostnetwork.Service, bool, error) {
	result, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT a.address,p.cidr,a.pool_id FROM network_allocations a JOIN network_pools p ON p.id=a.pool_id WHERE a.stack_id=?`, Params: []interface{}{stackID}})
	if err != nil {
		return hostnetwork.Service{}, false, err
	}
	if len(result.Values) == 0 {
		return hostnetwork.Service{}, false, nil
	}
	row := result.Values[0]
	ip, _ := row[0].(string)
	cidr, _ := row[1].(string)
	poolID, _ := row[2].(string)
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return hostnetwork.Service{}, false, err
	}
	return hostnetwork.Service{StackID: stackID, PoolID: poolID, Address: fmt.Sprintf("%s/%d", ip, prefix.Bits())}, true, nil
}
func CheckStackDestination(ctx context.Context, db *rqlite.Client, hosts *hostclient.Pool, stackID, node string) error {
	service, ok, err := StackService(ctx, db, stackID)
	if err != nil || !ok {
		return err
	}
	pools, err := ListPools(ctx, db)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		if pool.ID == service.PoolID {
			return Eligible(ctx, hosts, pool, node)
		}
	}
	return fmt.Errorf("ipam: allocation pool missing")
}

// RestoreNodeServices uses cluster allocation ownership after reboot. A host must
// not blindly replay its old addresses after another node has taken them over.
func RestoreNodeServices(ctx context.Context, db *rqlite.Client, hosts *hostclient.Pool, node string) error {
	rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT a.stack_id FROM network_allocations a JOIN stack_runtime r ON r.stack_id=a.stack_id WHERE r.active_node_id=? AND r.deleted_at IS NULL AND a.state IN ('pending','active')`, Params: []interface{}{node}})
	if err != nil {
		return err
	}
	services := []hostnetwork.Service{}
	for _, row := range rows.Values {
		stackID, _ := row[0].(string)
		service, ok, err := StackService(ctx, db, stackID)
		if err != nil {
			return err
		}
		if ok {
			if err = CheckStackDestination(ctx, db, hosts, stackID, node); err != nil {
				return err
			}
			services = append(services, service)
		}
	}
	_, err = hosts.Execute(ctx, node, "net.service.sync", services)
	return err
}

func SelectPool(pools []Pool, name, defaultID, node string) (*Pool, error) {
	for i := range pools {
		pool := &pools[i]
		if name != "" && pool.Name == name || name == "" && defaultID != "" && pool.ID == defaultID {
			if !Member(*pool, node) {
				return nil, fmt.Errorf("ipam: selected pool is not eligible for node %s", node)
			}
			return pool, nil
		}
	}
	return nil, fmt.Errorf("ipam: no eligible named or host-default network pool")
}
