package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/floatlab/floatlab-core/pkg/alerts"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"go.uber.org/zap"
)

const peerReachabilityRuleID = "node.unreachable"
const peerProbeFreshness = 2 * time.Minute

type peerAPI struct {
	reachable bool
	healthy   bool
}

type peerObservation struct {
	nodeID string
	at     time.Time
	icmp   *peerICMP
	api    *peerAPI
}

func (s *Server) runPeerReachability(ctx context.Context) {
	if s.startup.peers == nil || s.peerWorkloads == nil {
		return
	}
	ticker := time.NewTicker(alerts.Interval)
	defer ticker.Stop()
	for {
		if err := s.collectPeerReachability(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("peer reachability collection failed", zap.Error(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) collectPeerReachability(ctx context.Context) error {
	peers := s.startup.peers()
	observations := make([]peerObservation, len(peers))
	var probes sync.WaitGroup
	slots := make(chan struct{}, 8)
	for i, peer := range peers {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			probes.Wait()
			return ctx.Err()
		}
		probes.Go(func() {
			defer func() { <-slots }()
			origin, _ := s.peerWorkloads.lookup(peer)
			observations[i] = probeLabPeer(ctx, s.peerWorkloads.transport, peer, origin)
		})
	}
	probes.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	var metrics strings.Builder
	for _, sample := range observations {
		sample.writeMetrics(&metrics, s.cfg.LocalNodeID)
	}
	var metricsErr error
	if metrics.Len() != 0 {
		upload, cancel := context.WithTimeout(ctx, 3*time.Second)
		metricsErr = s.vmets.ImportPrometheus(upload, metrics.String())
		cancel()
	}
	return errors.Join(metricsErr, s.evaluatePeerReachability(ctx, observations))
}

func probeLabPeer(ctx context.Context, transport http.RoundTripper, nodeID, origin string) peerObservation {
	sample := peerObservation{nodeID: nodeID}
	endpoint, err := url.Parse(origin)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil {
		sample.at = time.Now().UTC()
		return sample
	}
	probe, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var probes sync.WaitGroup
	probes.Go(func() {
		sample.icmp, _ = probePeerICMP(probe, endpoint.Hostname())
	})
	probes.Go(func() {
		apiURL := endpoint.ResolveReference(&url.URL{Path: "/api/v1/health"})
		sample.api = probePeerAPI(probe, transport, apiURL, nodeID)
	})
	probes.Wait()
	sample.at = time.Now().UTC()
	return sample
}

func probePeerAPI(ctx context.Context, transport http.RoundTripper, endpoint *url.URL, nodeID string) *peerAPI {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		if peerNetworkFailure(err) {
			return &peerAPI{}
		}
		return nil
	}
	defer resp.Body.Close()
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 || resp.TLS.PeerCertificates[0].Subject.CommonName != nodeID {
		return nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return &peerAPI{reachable: true, healthy: resp.StatusCode == http.StatusOK}
}

func (sample peerObservation) writeMetrics(body *strings.Builder, observer string) {
	labels := fmt.Sprintf(`{observer_id=%q,node_id=%q}`, observer, sample.nodeID)
	write := func(name string, value float64) {
		fmt.Fprintf(body, "fl_lab_peer_%s%s %g %d\n", name, labels, value, sample.at.UnixMilli())
	}
	truth := func(name string, value bool) {
		if value {
			write(name, 1)
		} else {
			write(name, 0)
		}
	}
	truth("icmp_observed", sample.icmp != nil)
	truth("api_observed", sample.api != nil)
	if sample.icmp != nil {
		truth("icmp_reachable", sample.icmp.replies > 0)
		write("icmp_packet_loss_percent", sample.icmp.lossPercent())
		if sample.icmp.replies > 0 {
			write("icmp_rtt_seconds", sample.icmp.rtt.Seconds())
		}
	}
	if sample.api != nil {
		truth("api_reachable", sample.api.reachable)
		truth("api_healthy", sample.api.healthy)
	}
}

func (s *Server) evaluatePeerReachability(ctx context.Context, observations []peerObservation) error {
	rule, err := s.builtInAlertRule(ctx, peerReachabilityRuleID)
	if err != nil {
		return err
	}
	warningFor, err := ruleDuration(rule.ForDuration, true)
	if err != nil {
		return err
	}
	criticalFor, err := ruleDuration(rule.CriticalDuration, true)
	if err != nil {
		return err
	}
	recoveryFor, err := ruleDuration(rule.RecoveryDuration, true)
	if err != nil {
		return err
	}
	notifyAfter := rule.Thresholds["notify_after_failures"]
	if notifyAfter < 1 || notifyAfter != float64(int(notifyAfter)) {
		return errors.New("node reachability notification threshold must be a positive integer")
	}
	rows, err := s.db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT state_json FROM monitoring_checks WHERE producer='peer-reachability'`})
	if err != nil {
		return err
	}
	previous := make(map[string]monitoring.Check, len(rows.Values))
	for _, row := range rows.Values {
		var check monitoring.Check
		if err := json.Unmarshal([]byte(str(row[0])), &check); err != nil {
			return err
		}
		previous[check.Target] = check
	}
	var failures []error
	for _, observation := range observations {
		old := previous[observation.nodeID]
		delete(previous, observation.nodeID)
		if !rule.Enabled && old.Status == "disabled" {
			continue
		}
		transition := peerReachabilityTransition(s.cfg.LocalNodeID, observation, old, rule.Enabled, int(notifyAfter), warningFor, criticalFor, recoveryFor)
		failures = append(failures, monitoring.Record(ctx, s.db, s.broker, transition))
	}
	for node, old := range previous {
		if old.Status == "disabled" || node == "" {
			continue
		}
		transition := peerReachabilityTransition(s.cfg.LocalNodeID, peerObservation{nodeID: node, at: time.Now().UTC()}, old, false, int(notifyAfter), warningFor, criticalFor, recoveryFor)
		transition.Result.Reason = "lab member removed"
		transition.Result.Evidence = old.Evidence
		failures = append(failures, monitoring.Record(ctx, s.db, s.broker, transition))
	}
	return errors.Join(failures...)
}

func peerReachabilityTransition(observer string, sample peerObservation, old monitoring.Check, enabled bool, notifyAfter int, warningFor, criticalFor, recoveryFor time.Duration) monitoring.Transition {
	evidence := map[string]interface{}{
		"icmp_observed": sample.icmp != nil,
		"api_observed":  sample.api != nil,
	}
	if sample.icmp != nil {
		evidence["icmp_reachable"] = sample.icmp.replies > 0
		evidence["icmp_packet_loss_percent"] = sample.icmp.lossPercent()
		if sample.icmp.replies > 0 {
			evidence["icmp_rtt_seconds"] = sample.icmp.rtt.Seconds()
		}
	}
	if sample.api != nil {
		evidence["api_reachable"] = sample.api.reachable
		evidence["api_healthy"] = sample.api.healthy
	}
	result := monitoring.Result{
		CheckID:     "peer-reachability:" + sample.nodeID,
		Producer:    "peer-reachability",
		Kind:        "reachability",
		Resource:    monitoring.Resource{HostID: sample.nodeID},
		Observer:    observer,
		Target:      sample.nodeID,
		EvaluatedAt: sample.at,
		Labels:      map[string]string{"observer_id": observer, "node_id": sample.nodeID, "rule_id": peerReachabilityRuleID},
		Evidence:    evidence,
		Status:      "unknown",
		Reason:      "incomplete ICMP and control plane API observations",
	}
	transition := monitoring.Transition{Result: result, RuleID: peerReachabilityRuleID, Title: "Lab member unreachable", Severity: "warning", Action: "keep"}
	if !enabled {
		transition.Result.Status, transition.Result.Reason = "disabled", "rule disabled"
		transition.Action = "retire"
		return transition
	}
	transition.Result.ObservedAt = sample.at
	transition.Result.ExpiresAt = sample.at.Add(peerProbeFreshness)
	continuous := !old.ObservedAt.IsZero() && sample.at.After(old.ObservedAt) && !sample.at.After(old.ExpiresAt)
	switch {
	case sample.icmp != nil && sample.icmp.replies > 0 || sample.api != nil && sample.api.reachable:
		transition.Result.Status, transition.Result.Reason = "healthy", "lab member reachable"
		evidence["consecutive_failures"] = 0
		if old.IncidentID != "" {
			recoveredSince := sample.at
			if continuous && old.Status == "healthy" {
				if since, err := time.Parse(time.RFC3339Nano, str(old.Evidence["recovered_since"])); err == nil {
					recoveredSince = since
				}
			}
			evidence["recovered_since"] = recoveredSince.Format(time.RFC3339Nano)
			if sample.at.Sub(recoveredSince) >= recoveryFor {
				transition.Action, transition.Notify = "resolve", true
				transition.Message = fmt.Sprintf("Lab member %s is reachable again from %s.", sample.nodeID, observer)
				if old.Severity != "" {
					transition.Severity = old.Severity
				}
			}
		}
	case sample.icmp != nil && sample.api != nil:
		transition.Result.Status, transition.Result.Reason = "failing", "ICMP echo and control plane API probes failed"
		failedSince, failedChecks := sample.at, 1
		if continuous && old.Status == "failing" {
			if since, err := time.Parse(time.RFC3339Nano, str(old.Evidence["failed_since"])); err == nil {
				failedSince = since
			}
			failedChecks += number(old.Evidence["consecutive_failures"])
		}
		evidence["failed_since"] = failedSince.Format(time.RFC3339Nano)
		evidence["consecutive_failures"] = failedChecks
		evidence["notify_after_failures"] = notifyAfter
		evidence["next_probe_at"] = sample.at.Add(alerts.Interval).Format(time.RFC3339Nano)
		elapsed := sample.at.Sub(failedSince)
		critical := elapsed >= criticalFor || old.Severity == "critical"
		if critical || old.IncidentID != "" || elapsed >= warningFor {
			transition.Action = "open"
			transition.Notify = critical || failedChecks >= notifyAfter
			if critical {
				transition.Severity = "critical"
			}
			transition.Message = fmt.Sprintf("Lab member %s failed %d consecutive ICMP and control plane API checks from %s. Check the member and its network connection.", sample.nodeID, failedChecks, observer)
		}
	}
	return transition
}
