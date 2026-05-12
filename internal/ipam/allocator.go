package ipam

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/google/uuid"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Allocate reserves the next available IPv6 address in the given network prefix
// for the specified stack and service. The address is stored in ip_reservations.
// Returns the allocated address in CIDR notation (e.g., "fd00::3/64").
func Allocate(ctx context.Context, db *rqlite.Client, networkPrefix, stackID, service string) (string, error) {
	prefix, err := netip.ParsePrefix(networkPrefix)
	if err != nil {
		return "", fmt.Errorf("ipam: parse prefix %q: %w", networkPrefix, err)
	}
	prefix = prefix.Masked()

	// Find all addresses already allocated within this prefix.
	res, err := db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT address FROM ip_reservations WHERE prefix_pool = ? ORDER BY address`,
		Params: []interface{}{networkPrefix},
	})
	if err != nil {
		return "", fmt.Errorf("ipam: list reservations: %w", err)
	}

	taken := make(map[netip.Addr]struct{}, len(res.Values))
	for _, row := range res.Values {
		if addrStr, ok := row[0].(string); ok {
			if p, err := netip.ParsePrefix(addrStr); err == nil {
				taken[p.Addr()] = struct{}{}
			}
		}
	}

	// Walk upward from network+1 to find the first free address.
	addr := prefix.Addr().Next() // skip network address
	bits := prefix.Bits()
	for {
		if !prefix.Contains(addr) {
			return "", fmt.Errorf("ipam: prefix %s is exhausted", networkPrefix)
		}
		if _, used := taken[addr]; !used {
			break
		}
		addr = addr.Next()
	}

	cidr := netip.PrefixFrom(addr, bits).String()
	id := uuid.New().String()
	if err := db.Execute(ctx, []rqlite.Statement{{
		SQL: `INSERT INTO ip_reservations(id, stack_id, service, address, prefix_pool, allocated_at)
		      VALUES(?, ?, ?, ?, ?, datetime('now'))`,
		Params: []interface{}{id, stackID, service, cidr, networkPrefix},
	}}); err != nil {
		return "", fmt.Errorf("ipam: insert reservation: %w", err)
	}
	return cidr, nil
}

// Release removes all IP reservations for the given stack and service.
func Release(ctx context.Context, db *rqlite.Client, stackID, service string) error {
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM ip_reservations WHERE stack_id = ? AND service = ?`,
		Params: []interface{}{stackID, service},
	}})
}

// ReleaseAll removes all IP reservations for the given stack.
func ReleaseAll(ctx context.Context, db *rqlite.Client, stackID string) error {
	return db.Execute(ctx, []rqlite.Statement{{
		SQL:    `DELETE FROM ip_reservations WHERE stack_id = ?`,
		Params: []interface{}{stackID},
	}})
}

// ListAllocations returns all IP reservations for the given stack.
type Reservation struct {
	ID          string `json:"id"`
	StackID     string `json:"stack_id"`
	Service     string `json:"service"`
	Address     string `json:"address"`
	PrefixPool  string `json:"prefix_pool"`
	AllocatedAt string `json:"allocated_at"`
}

func ListAllocations(ctx context.Context, db *rqlite.Client, stackID string) ([]Reservation, error) {
	res, err := db.Query(ctx, rqlite.Statement{
		SQL:    `SELECT id, stack_id, service, address, prefix_pool, allocated_at FROM ip_reservations WHERE stack_id = ? ORDER BY service`,
		Params: []interface{}{stackID},
	})
	if err != nil {
		return nil, fmt.Errorf("ipam: list allocations: %w", err)
	}
	out := make([]Reservation, 0, len(res.Values))
	for _, row := range res.Values {
		r := Reservation{}
		r.ID, _ = row[0].(string)
		r.StackID, _ = row[1].(string)
		r.Service, _ = row[2].(string)
		r.Address, _ = row[3].(string)
		r.PrefixPool, _ = row[4].(string)
		r.AllocatedAt, _ = row[5].(string)
		out = append(out, r)
	}
	return out, nil
}
