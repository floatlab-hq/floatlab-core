package control

import (
	"context"
	"fmt"
	"time"

	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/notify"
	"github.com/floatlab/floatlab-core/pkg/release"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
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
	now := time.Now().UTC()
	status, action := "unknown", "keep"
	switch result.Status {
	case "compatible":
		status, action = "healthy", "resolve"
	case "incompatible":
		status, action = "failing", "open"
	}
	body := fmt.Sprintf("Management %s requires host >=%s <%s; host %s reports %q (%s). %s", release.Version, result.Required.Min, result.Required.Max, node, result.HostVersion, result.Status, result.Error)
	return monitoring.Record(ctx, db, broker, monitoring.Transition{Result: monitoring.Result{CheckID: "host-compatibility:" + node, Producer: "host-compatibility", Kind: "host_compatibility", Resource: monitoring.Resource{HostID: node}, Observer: node, Target: node, Status: status, Reason: result.Error, ObservedAt: now, EvaluatedAt: now, Evidence: map[string]interface{}{"host_version": result.HostVersion, "required": result.Required}}, RuleID: "host_compatibility", Title: "Host version compatibility", Message: body, Severity: "warning", Action: action, Notify: true})
}
