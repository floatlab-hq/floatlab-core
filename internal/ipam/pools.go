package ipam

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type Pool struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CIDR      string    `json:"cidr"`
	StartIP   string    `json:"start_ip"`
	EndIP     string    `json:"end_ip"`
	Default   bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func ValidatePool(pool Pool) error {
	prefix, err := netip.ParsePrefix(pool.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() {
		return fmt.Errorf("ipam: cidr must be a canonical IPv4 prefix")
	}
	start, startErr := netip.ParseAddr(pool.StartIP)
	end, endErr := netip.ParseAddr(pool.EndIP)
	if pool.Name == "" || startErr != nil || endErr != nil || !start.Is4() || !end.Is4() || !prefix.Contains(start) || !prefix.Contains(end) || start.Compare(end) > 0 {
		return fmt.Errorf("ipam: invalid pool range")
	}
	network := prefix.Addr()
	broadcast := lastAddress(prefix)
	if start == network || end == broadcast {
		return fmt.Errorf("ipam: range cannot contain network or broadcast address")
	}
	return nil
}

func ListPools(ctx context.Context, db *rqlite.Client) ([]Pool, error) {
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT id,name,cidr,start_ip,end_ip,is_default,created_at,updated_at FROM network_pools ORDER BY name`})
	if err != nil {
		return nil, err
	}
	pools := make([]Pool, 0, len(result.Values))
	for _, row := range result.Values {
		pool := Pool{}
		pool.ID, _ = row[0].(string)
		pool.Name, _ = row[1].(string)
		pool.CIDR, _ = row[2].(string)
		pool.StartIP, _ = row[3].(string)
		pool.EndIP, _ = row[4].(string)
		pool.Default = number(row[5]) == 1
		if value, ok := row[6].(string); ok {
			pool.CreatedAt, _ = time.Parse(time.RFC3339, value)
		}
		if value, ok := row[7].(string); ok {
			pool.UpdatedAt, _ = time.Parse(time.RFC3339, value)
		}
		pools = append(pools, pool)
	}
	return pools, nil
}

func SavePool(ctx context.Context, db *rqlite.Client, pool *Pool) error {
	if err := ValidatePool(*pool); err != nil {
		return err
	}
	pools, err := ListPools(ctx, db)
	if err != nil {
		return err
	}
	wanted, _ := netip.ParsePrefix(pool.CIDR)
	for _, existing := range pools {
		if existing.ID == pool.ID {
			continue
		}
		prefix, _ := netip.ParsePrefix(existing.CIDR)
		if wanted.Contains(prefix.Addr()) || prefix.Contains(wanted.Addr()) {
			return fmt.Errorf("ipam: pool overlaps %s", existing.Name)
		}
	}
	if pool.ID != "" {
		allocations, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT address FROM network_allocations WHERE pool_id=?`, Params: []interface{}{pool.ID}})
		if err != nil {
			return err
		}
		start, _ := netip.ParseAddr(pool.StartIP)
		end, _ := netip.ParseAddr(pool.EndIP)
		for _, row := range allocations.Values {
			value, _ := row[0].(string)
			address, parseErr := netip.ParseAddr(value)
			if parseErr != nil || !wanted.Contains(address) || address.Compare(start) < 0 || address.Compare(end) > 0 {
				return fmt.Errorf("ipam: pool update would exclude active allocation %s", value)
			}
		}
	}
	now := time.Now().UTC()
	if pool.ID == "" {
		pool.ID, pool.CreatedAt = uuid.NewString(), now
	}
	pool.UpdatedAt = now
	statements := []rqlite.Statement{}
	if pool.Default {
		statements = append(statements, rqlite.Statement{SQL: `UPDATE network_pools SET is_default=0 WHERE is_default=1`})
	}
	statements = append(statements, rqlite.Statement{
		SQL: `INSERT INTO network_pools(id,name,cidr,start_ip,end_ip,is_default,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?)
		      ON CONFLICT(id) DO UPDATE SET name=excluded.name,cidr=excluded.cidr,start_ip=excluded.start_ip,end_ip=excluded.end_ip,is_default=excluded.is_default,updated_at=excluded.updated_at`,
		Params: []interface{}{pool.ID, pool.Name, pool.CIDR, pool.StartIP, pool.EndIP, pool.Default, pool.CreatedAt, pool.UpdatedAt},
	})
	return db.Execute(ctx, statements)
}

func DeletePool(ctx context.Context, db *rqlite.Client, id string) error {
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT 1 FROM network_allocations WHERE pool_id=? LIMIT 1`, Params: []interface{}{id}})
	if err != nil {
		return err
	}
	if len(result.Values) > 0 {
		return fmt.Errorf("ipam: network pool has active allocations")
	}
	return db.Execute(ctx, []rqlite.Statement{{
		SQL: `DELETE FROM network_pools WHERE id=? AND NOT EXISTS(SELECT 1 FROM network_allocations WHERE pool_id=?)`, Params: []interface{}{id, id},
	}})
}

func AllocateIPv4(ctx context.Context, db *rqlite.Client, pool Pool, stackID string) (string, error) {
	if err := ValidatePool(pool); err != nil {
		return "", err
	}
	result, err := db.Query(ctx, rqlite.Statement{SQL: `SELECT address FROM network_allocations WHERE pool_id=?`, Params: []interface{}{pool.ID}})
	if err != nil {
		return "", err
	}
	taken := map[netip.Addr]bool{}
	for _, row := range result.Values {
		if value, ok := row[0].(string); ok {
			if address, err := netip.ParseAddr(value); err == nil {
				taken[address] = true
			}
		}
	}
	start, _ := netip.ParseAddr(pool.StartIP)
	end, _ := netip.ParseAddr(pool.EndIP)
	for address := start; address.Compare(end) <= 0; address = address.Next() {
		if taken[address] {
			continue
		}
		now := time.Now().UTC()
		err := db.Execute(ctx, []rqlite.Statement{{
			SQL:    `INSERT INTO network_allocations(id,pool_id,stack_id,address,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?)`,
			Params: []interface{}{uuid.NewString(), pool.ID, stackID, address.String(), "pending", now, now},
		}})
		if err == nil {
			return address.String(), nil
		}
	}
	return "", fmt.Errorf("ipam: pool exhausted")
}

func ActivateIPv4(ctx context.Context, db *rqlite.Client, stackID string) error {
	now := time.Now().UTC()
	return db.Execute(ctx, []rqlite.Statement{
		{SQL: `UPDATE network_allocations SET state='active',updated_at=? WHERE stack_id=? AND state='pending'`, Params: []interface{}{now, stackID}},
		{SQL: `INSERT INTO dns_outbox(id,stack_id,type,payload,created_at) SELECT ?,stack_id,'address.allocated',json_object('address',address),? FROM network_allocations WHERE stack_id=?`, Params: []interface{}{uuid.NewString(), now, stackID}},
	})
}

func ReleaseIPv4(ctx context.Context, db *rqlite.Client, stackID string) error {
	return db.Execute(ctx, []rqlite.Statement{{SQL: `DELETE FROM network_allocations WHERE stack_id=?`, Params: []interface{}{stackID}}})
}

func lastAddress(prefix netip.Prefix) netip.Addr {
	address := prefix.Addr().As4()
	bits := uint32(32 - prefix.Bits())
	value := uint32(address[0])<<24 | uint32(address[1])<<16 | uint32(address[2])<<8 | uint32(address[3])
	value |= uint32(1<<bits) - 1
	return netip.AddrFrom4([4]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
}

func number(value interface{}) int {
	switch value := value.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	}
	return 0
}
