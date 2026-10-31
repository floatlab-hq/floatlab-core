package lab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

type RemovalPreview struct {
	Member              Member                `json:"member"`
	RemainingVoters     int                   `json:"remaining_voters"`
	Workloads           []map[string]any      `json:"workloads"`
	RecoveryPoints      []map[string]any      `json:"recovery_points"`
	AddressReservations []map[string]any      `json:"address_reservations"`
	ReplicaIdentity     *rqlite.LocalIdentity `json:"replica_identity,omitempty"`
	ObservedAt          time.Time             `json:"observed_at"`
	Stale               bool                  `json:"stale"`
	Warnings            []string              `json:"warnings"`
}

func (s *Service) RemovalPreview(ctx context.Context, nodeID string) (RemovalPreview, error) {
	preview := RemovalPreview{Workloads: []map[string]any{}, RecoveryPoints: []map[string]any{}, AddressReservations: []map[string]any{}, Warnings: []string{"Removal does not stop workloads, purge backups or release workload address reservations."}}
	if !validID.MatchString(nodeID) {
		return preview, errors.New("invalid member ID")
	}
	_ = s.RefreshTrust(ctx)
	for _, m := range s.Snapshot().Members {
		if m.NodeID == nodeID {
			preview.Member = m
		} else if m.Status == "active" {
			preview.RemainingVoters++
		}
	}
	if preview.Member.NodeID == "" {
		return preview, errors.New("unknown member")
	}
	var db *rqlite.Client
	if nodeID == s.cfg.NodeID {
		db = s.cfg.LocalDB
		preview.ObservedAt = time.Now().UTC()
	} else {
		preview.Stale = true
		if s.cfg.ReplicaDir != "" {
			path := filepath.Join(s.cfg.ReplicaDir, nodeID+".sqlite")
			var err error
			db, err = rqlite.OpenReplica(path)
			if err != nil {
				preview.Warnings = append(preview.Warnings, "No readable workload replica: "+err.Error())
			} else {
				defer db.Close()
				identity, e := rqlite.ReadReplicaIdentity(ctx, path)
				if e == nil {
					preview.ReplicaIdentity = &identity
				}
				if info, e := os.Stat(path); e == nil {
					preview.ObservedAt = info.ModTime().UTC()
				}
			}
		}
		preview.Warnings = append(preview.Warnings, "Remote workload inventory is a cached view and may omit recent commits.")
	}
	if db == nil {
		preview.Warnings = append(preview.Warnings, "Workload and backup inventory unavailable; do not assume the node is empty.")
	} else {
		for _, query := range []struct {
			sql string
			out *[]map[string]any
		}{{`SELECT id,name,primary_node_id,backup_node_id,zfs_dataset,backup_target FROM stacks`, &preview.Workloads}, {`SELECT id,stack_id,snapshot_id,dataset_path,created_at,deleted_at FROM recovery_points`, &preview.RecoveryPoints}, {`SELECT pool_id,stack_id,address,state FROM network_allocations`, &preview.AddressReservations}} {
			rows, err := db.Query(ctx, rqlite.Statement{SQL: query.sql})
			if err != nil {
				preview.Warnings = append(preview.Warnings, err.Error())
				continue
			}
			*query.out = records(rows)
		}
	}
	rows, err := s.cfg.DB.Query(ctx, rqlite.Statement{SQL: `SELECT p.id,p.name,p.cidr,p.start_ip,p.end_ip FROM network_pools p JOIN network_pool_nodes n ON n.pool_id=p.id WHERE n.node_id=?`, Params: []interface{}{nodeID}})
	if err != nil {
		preview.Warnings = append(preview.Warnings, "Shared address reservations unavailable: "+err.Error())
	} else {
		preview.AddressReservations = append(preview.AddressReservations, records(rows)...)
	}
	retained, err := s.cfg.DB.Query(ctx, rqlite.Statement{SQL: `SELECT pool_id,start,end,owner AS node_id,'retained' AS state FROM node_pool_ranges WHERE owner=? AND NOT EXISTS(SELECT 1 FROM network_pools WHERE id=pool_id)`, Params: []any{nodeID}})
	if err != nil {
		preview.Warnings = append(preview.Warnings, "Retained range inventory unavailable: "+err.Error())
	} else {
		preview.AddressReservations = append(preview.AddressReservations, records(retained)...)
	}
	return preview, nil
}

func records(rows *rqlite.QueryResult) []map[string]any {
	out := make([]map[string]any, 0, len(rows.Values))
	for _, row := range rows.Values {
		record := map[string]any{}
		for i, name := range rows.Columns {
			if i < len(row) {
				record[name] = row[i]
			}
		}
		out = append(out, record)
	}
	return out
}

func decodeMember(value any, m *Member) error { return json.Unmarshal([]byte(fmt.Sprint(value)), m) }

type ReleasePeerAddressRequest struct {
	OperationID   string `json:"operation_id"`
	ConfirmNodeID string `json:"confirm_node_id"`
	ConfirmFenced bool   `json:"confirm_fenced"`
	ConfirmReset  bool   `json:"confirm_reset"`
}

func (s *Service) ReleasePeerAddress(ctx context.Context, nodeID string, req ReleasePeerAddressRequest) (Member, error) {
	s.operations.Lock()
	defer s.operations.Unlock()
	if req.ConfirmNodeID != nodeID || !validID.MatchString(req.OperationID) || !req.ConfirmFenced || !req.ConfirmReset {
		return Member{}, errors.New("operation ID, matching identity, fencing and empty-appliance reset confirmation required")
	}
	r, err := s.cfg.DB.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT body FROM lab_members WHERE node_id=?`, Params: []interface{}{nodeID}})
	if err != nil {
		return Member{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if len(r.Values) != 1 {
		return Member{}, errors.New("unknown member")
	}
	var m Member
	if err = decodeMember(r.Values[0][0], &m); err != nil {
		return m, err
	}
	if m.Status != "removed" {
		return m, errors.New("only a completed tombstone can release its peer address")
	}
	if m.PeerAddressReleased {
		return m, nil
	}
	m.PeerAddressReleased = true
	if err = s.putMember(ctx, m); err != nil {
		return m, err
	}
	return m, s.RefreshTrust(ctx)
}
