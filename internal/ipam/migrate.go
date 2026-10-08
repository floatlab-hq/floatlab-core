package ipam

import (
	"context"
	"fmt"

	"github.com/floatlab/floatlab-core/internal/hostnetwork"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

// Legacy subnet inference is a one-time migration only. New pools require
// administrator-declared membership, never automatic subnet inference.
func MigratePools(ctx context.Context, db *rqlite.Client, hosts *hostclient.Pool) error {
	pools, err := ListPools(ctx, db)
	if err != nil {
		return err
	}
	nodes, err := config.NewStore(db).ListNodes(ctx)
	if err != nil {
		return err
	}
	for _, pool := range pools {
		if pool.MembershipResolved && !pool.Default {
			continue
		}
		members := append([]string{}, pool.NodeIDs...)
		resolved := pool.MembershipResolved
		if !pool.MembershipResolved {
			members = []string{}
			resolved = true
		}
		if !pool.MembershipResolved {
			for _, node := range nodes {
				status, err := HostStatus(ctx, hosts, node.ID)
				if err != nil || status.Change != nil {
					resolved = false
					continue
				}
				if hostnetwork.Compatible(pool.CIDR, status.Live.PrimaryAddresses) {
					members = append(members, node.ID)
				}
			}
		}
		active, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT DISTINCT active_node_id FROM stack_runtime WHERE network_pool=? AND stack_ip IS NOT NULL AND deleted_at IS NULL`, Params: []interface{}{pool.ID}})
		if err != nil {
			return err
		}
		for _, row := range active.Values {
			node, _ := row[0].(string)
			if !containsNode(members, node) {
				members = append(members, node)
				resolved = false
			}
		}
		if len(members) == 0 {
			resolved = false
		}
		statements := []rqlite.Statement{{SQL: `DELETE FROM network_pool_nodes WHERE pool_id=?`, Params: []interface{}{pool.ID}}}
		for _, node := range members {
			statements = append(statements, rqlite.Statement{SQL: `INSERT OR IGNORE INTO network_pool_nodes(pool_id,node_id) VALUES(?,?)`, Params: []interface{}{pool.ID, node}})
		}
		statements = append(statements, rqlite.Statement{SQL: `INSERT INTO network_pool_scope(pool_id,resolved) VALUES(?,?) ON CONFLICT(pool_id) DO UPDATE SET resolved=excluded.resolved`, Params: []interface{}{pool.ID, resolved}})
		if err = db.Execute(ctx, statements); err != nil {
			return err
		}
		if pool.Default && resolved {
			for _, node := range members {
				if _, err = hosts.Execute(ctx, node, "net.settings.default.migrate", map[string]string{"pool_id": pool.ID}); err != nil {
					return fmt.Errorf("migrate default on %s: %w", node, err)
				}
			}
			if err = db.Execute(ctx, []rqlite.Statement{{SQL: `UPDATE network_pools SET is_default=0 WHERE id=?`, Params: []interface{}{pool.ID}}}); err != nil {
				return err
			}
		}
	}
	return nil
}
