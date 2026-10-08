package control

import (
	"fmt"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
	"math"
	"net/http"
	"net/netip"
	"time"
)

const prefixProjection = `SELECT id,name,prefix,stack_id,created_at FROM prefix_pools`

func (s *Server) handleListPrefixPools(w http.ResponseWriter, r *http.Request) {
	rows, err := s.queryObjects(r, `SELECT p.id,p.name,p.prefix,p.stack_id,p.created_at,count(i.id) AS allocated_addresses FROM prefix_pools p LEFT JOIN ip_reservations i ON i.prefix_pool=p.id GROUP BY p.id ORDER BY p.name`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, row := range rows {
		prefix, _ := netip.ParsePrefix(fmt.Sprint(row["prefix"]))
		var capacity int64 = math.MaxInt64
		if prefix.IsValid() && 128-prefix.Bits() < 63 {
			capacity = int64(1) << uint(128-prefix.Bits())
		}
		row["total_addresses"] = capacity
	}
	writeJSON(w, 200, rows)
}
func (s *Server) handleCreatePrefixPool(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Prefix  string `json:"prefix"`
		StackID string `json:"stack_id"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	prefix, err := netip.ParsePrefix(body.Prefix)
	if err != nil || !prefix.Addr().Is6() || body.Name == "" {
		writeError(w, 400, "name and valid IPv6 prefix are required")
		return
	}
	rows, err := s.queryObjects(r, prefixProjection)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	for _, row := range rows {
		other, _ := netip.ParsePrefix(fmt.Sprint(row["prefix"]))
		if row["name"] == body.Name || (other.IsValid() && other.Overlaps(prefix)) {
			writeError(w, 409, "prefix overlaps an existing pool")
			return
		}
	}
	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `INSERT INTO prefix_pools(id,name,prefix,stack_id,created_at) VALUES(?,?,?,?,?)`, Params: []interface{}{id, body.Name, prefix.Masked().String(), body.StackID, now}}}); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 201, map[string]interface{}{"id": id, "name": body.Name, "prefix": prefix.Masked().String(), "stack_id": body.StackID, "created_at": now, "allocated_addresses": 0})
}
func (s *Server) handleCreateAllocation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		StackID string `json:"stack_id"`
		Service string `json:"service"`
		Pool    string `json:"prefix_pool"`
		Address string `json:"address"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.StackID == "" || body.Service == "" || body.Pool == "" {
		writeError(w, 400, "stack_id, service and prefix_pool are required")
		return
	}
	if _, err := s.store.GetStack(r.Context(), body.StackID); err != nil {
		writeError(w, 404, "stack not found")
		return
	}
	pools, err := s.queryObjects(r, prefixProjection+" WHERE id=?", body.Pool)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	if len(pools) == 0 {
		writeError(w, 404, "prefix pool not found")
		return
	}
	pool := pools[0]
	if stack, _ := pool["stack_id"].(string); stack != "" && stack != body.StackID {
		writeError(w, 409, "pool belongs to another stack")
		return
	}
	prefix, _ := netip.ParsePrefix(fmt.Sprint(pool["prefix"]))
	rows, err := s.queryObjects(r, `SELECT address FROM ip_reservations`)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	taken := map[netip.Addr]bool{}
	for _, row := range rows {
		address, _ := netip.ParsePrefix(fmt.Sprint(row["address"]))
		if address.IsValid() {
			taken[address.Addr()] = true
		}
	}
	address := prefix.Masked().Addr()
	if body.Address != "" {
		address, err = netip.ParseAddr(body.Address)
		if err != nil {
			requested, e := netip.ParsePrefix(body.Address)
			err = e
			address = requested.Addr()
		}
		if err != nil || !prefix.Contains(address) {
			writeError(w, 400, "address must be within pool")
			return
		}
	} else {
		for address.IsValid() && prefix.Contains(address) && taken[address] {
			address = address.Next()
		}
	}
	if !address.IsValid() || !prefix.Contains(address) || taken[address] {
		writeError(w, 409, "address reserved or pool exhausted")
		return
	}
	id := uuid.NewString()
	cidr := netip.PrefixFrom(address, prefix.Bits()).String()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if err := s.db.Execute(r.Context(), []rqlite.Statement{{SQL: `INSERT INTO ip_reservations(id,stack_id,service,address,prefix_pool,allocated_at) VALUES(?,?,?,?,?,?)`, Params: []interface{}{id, body.StackID, body.Service, cidr, body.Pool, now}}}); err != nil {
		writeError(w, 409, err.Error())
		return
	}
	writeJSON(w, 201, map[string]interface{}{"id": id, "stack_id": body.StackID, "service": body.Service, "address": cidr, "prefix_pool": body.Pool, "allocated_at": now})
}
