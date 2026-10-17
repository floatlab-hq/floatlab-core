package control

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// RunHostCompatibility reports advisory alerts without changing host operations.
func RunHostCompatibility(ctx context.Context, hosts *hostclient.Pool, db *rqlite.Client, broker *notify.Broker, log *zap.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		// Each controller owns alerts for its local sockets; the node database is shared.
		for _, id := range hosts.NodeIDs() {
			checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			result := release.CheckHost(checkCtx, hosts, id)
			cancel()
			if ctx.Err() != nil {
				return
			}
			if err := recordHostCompatibility(ctx, db, broker, id, result); err != nil {
				log.Warn("host compatibility alert", zap.String("node", id), zap.Error(err))
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func recordHostCompatibility(ctx context.Context, db *rqlite.Client, broker *notify.Broker, node string, result release.Compatibility) error {
	id := "host-compatibility:" + node
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if result.Status == "compatible" {
		if err := db.Execute(ctx, []rqlite.Statement{
			{SQL: `UPDATE alerts SET state='resolved',resolved_at=? WHERE id=? AND state='active'`, Params: []interface{}{now, id}},
			{SQL: `UPDATE notifications SET state='resolved',resolved_at=? WHERE alert_id=? AND resolved_at IS NULL`, Params: []interface{}{now, id}},
		}); err != nil {
			return err
		}
		return nil
	}
	body := fmt.Sprintf("Management %s requires host >=%s <%s; host %s reports %q (%s). %s", release.Version, result.Required.Min, result.Required.Max, node, result.HostVersion, result.Status, result.Error)
	n := notify.Notification{ID: uuid.NewString(), AlertID: id, NodeID: node, Kind: "alert", Severity: "warning", Title: "Host version compatibility", Body: body, State: "unread", CreatedAt: time.Now().UTC()}
	err := db.Execute(ctx, []rqlite.Statement{
		{SQL: `INSERT INTO notifications(id,alert_id,node_id,kind,severity,title,body,state,created_at) SELECT ?,?,?,'alert','warning',?,?,'unread',? WHERE NOT EXISTS(SELECT 1 FROM alerts WHERE id=? AND state='active')`, Params: []interface{}{n.ID, id, node, n.Title, body, now, id}},
		{SQL: `INSERT INTO alerts(id,rule_id,node_id,severity,kind,state,message,created_at) VALUES(?,'host_compatibility',?,'warning','host_compatibility','active',?,?) ON CONFLICT(id) DO UPDATE SET state='active',message=excluded.message,resolved_at=NULL`, Params: []interface{}{id, node, body, now}},
		{SQL: `UPDATE notifications SET body=? WHERE alert_id=? AND resolved_at IS NULL`, Params: []interface{}{body, id}},
	})
	if err != nil {
		return err
	}
	created, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT id FROM notifications WHERE id=?`, Params: []interface{}{n.ID}})
	if err != nil {
		return err
	}
	if len(created.Values) > 0 && broker != nil {
		raw, err := json.Marshal(n)
		if err != nil {
			return err
		}
		broker.PublishJSON("notification.new", raw)
	}
	return nil
}
