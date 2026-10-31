package control

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/floatlab/floatlab-core/internal/ipam"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func (s *Server) saveNetworkPool(ctx context.Context, pool *ipam.Pool) error {
	if s.cfg.LocalNodeID != "" {
		return ipam.SaveNodePool(ctx, s.db, pool, s.cfg.LocalNodeID)
	}
	return ipam.SavePool(ctx, s.db, pool)
}

func (s *Server) validateLocalPool(ctx context.Context, pool ipam.Pool) error {
	if s.cfg.LocalNodeID == "" {
		return nil
	}
	if len(pool.NodeIDs) != 1 || pool.NodeIDs[0] != s.cfg.LocalNodeID {
		return fmt.Errorf("independent workload allocation requires a range assigned exclusively to this node")
	}
	pools, err := ipam.ListPools(ctx, s.db)
	if err != nil {
		return err
	}
	start, startErr := netip.ParseAddr(pool.StartIP)
	end, endErr := netip.ParseAddr(pool.EndIP)
	if startErr != nil || endErr != nil {
		return fmt.Errorf("invalid address range")
	}
	for _, existing := range pools {
		if existing.ID == pool.ID {
			if len(existing.NodeIDs) != 1 || existing.NodeIDs[0] != s.cfg.LocalNodeID {
				return fmt.Errorf("range ownership cannot be transferred implicitly; its owner's reservations must be retired explicitly")
			}
			continue
		}
		otherStart, e1 := netip.ParseAddr(existing.StartIP)
		otherEnd, e2 := netip.ParseAddr(existing.EndIP)
		if e1 == nil && e2 == nil && start.Compare(otherEnd) <= 0 && otherStart.Compare(end) <= 0 {
			return fmt.Errorf("address range overlaps reserved pool %s", existing.Name)
		}
	}
	if pool.ID != "" && s.workloadDB != nil {
		rows, err := s.workloadDB.Query(ctx, rqlite.Statement{SQL: `SELECT address FROM network_allocations WHERE pool_id=?`, Params: []any{pool.ID}})
		if err != nil {
			return err
		}
		for _, row := range rows.Values {
			ip, err := netip.ParseAddr(fmt.Sprint(row[0]))
			if err != nil || ip.Compare(start) < 0 || ip.Compare(end) > 0 {
				return fmt.Errorf("range change excludes active local allocation")
			}
		}
	}
	return nil
}
