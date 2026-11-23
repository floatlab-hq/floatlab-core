package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/floatlab/floatlab-core/pkg/alerts"
	"github.com/floatlab/floatlab-core/pkg/monitoring"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestPeerAPIReachabilityUsesTargetIdentity(t *testing.T) {
	endpoint := &url.URL{Scheme: "https", Host: "peer.invalid", Path: "/api/v1/health"}
	transport := moveTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != endpoint.String() {
			t.Fatalf("unexpected probe request %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, TLS: &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{Subject: pkix.Name{CommonName: "known-member"}}},
		}}, nil
	})
	if result := probePeerAPI(context.Background(), transport, endpoint, "known-member"); result == nil || !result.reachable || result.healthy {
		t.Fatalf("expected a reachable but unhealthy API, got %+v", result)
	}
	if result := probePeerAPI(context.Background(), transport, endpoint, "different-member"); result != nil {
		t.Fatalf("unexpected observation for a different peer identity: %+v", result)
	}
}

func TestPeerReachabilityNotifiesOnSecondConsecutiveFailure(t *testing.T) {
	ctx := context.Background()
	_, db := peerWorkloadServer(t, "observing-node")
	if err := alerts.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var previous monitoring.Check
	check := func(sample peerObservation, expected string, notifications ...string) {
		t.Helper()
		sample.nodeID = "target-node"
		next := peerReachabilityTransition("observing-node", sample, previous, true, 2, 0, 5*time.Minute, 0)
		if err := monitoring.Record(ctx, db, nil, next); err != nil {
			t.Fatal(err)
		}
		row, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT state_json FROM monitoring_checks WHERE id=?`, Params: []interface{}{next.Result.CheckID}})
		if err != nil {
			t.Fatal(err)
		}
		previous = monitoring.Check{}
		if err := json.Unmarshal([]byte(str(row.Values[0][0])), &previous); err != nil {
			t.Fatal(err)
		}
		if previous.Status != expected || previous.Observer != "observing-node" || previous.Target != "target-node" {
			t.Fatalf("unexpected check: %+v", previous)
		}
		rows, err := db.QueryStrong(ctx, rqlite.Statement{SQL: `SELECT kind FROM notifications ORDER BY created_at`})
		if err != nil {
			t.Fatal(err)
		}
		var kinds []string
		for _, row := range rows.Values {
			kinds = append(kinds, str(row[0]))
		}
		if !reflect.DeepEqual(kinds, notifications) {
			t.Fatalf("got notifications %v, expected %v", kinds, notifications)
		}
	}
	now := time.Now().UTC()
	failing := peerObservation{at: now, icmp: &peerICMP{}, api: &peerAPI{}}
	check(failing, "failing")
	if previous.IncidentID == "" || previous.Severity != "warning" {
		t.Fatalf("first failed check must be a visible warning: %+v", previous)
	}
	check(peerObservation{at: now.Add(30 * time.Second), icmp: &peerICMP{}}, "unknown")
	failing.at = now.Add(time.Minute)
	check(failing, "failing")
	failing.at = now.Add(90 * time.Second)
	check(failing, "failing", "alert")
	failing.at = now.Add(2 * time.Minute)
	check(failing, "failing", "alert")
	check(peerObservation{at: now.Add(150 * time.Second), icmp: &peerICMP{}, api: &peerAPI{reachable: true}}, "healthy", "alert", "alert.resolved")
	failing.at = now.Add(3 * time.Minute)
	check(failing, "failing", "alert", "alert.resolved")
	check(peerObservation{at: now.Add(210 * time.Second), icmp: &peerICMP{replies: 1}, api: &peerAPI{}}, "healthy", "alert", "alert.resolved")
}

func TestPeerProbeMetricsIdentifyBothEndpoints(t *testing.T) {
	sample := peerObservation{nodeID: "target", at: time.UnixMilli(1234000), icmp: &peerICMP{replies: 3, rtt: 75 * time.Millisecond}}
	var output strings.Builder
	sample.writeMetrics(&output, "observer")
	want := `fl_lab_peer_icmp_observed{observer_id="observer",node_id="target"} 1 1234000
fl_lab_peer_api_observed{observer_id="observer",node_id="target"} 0 1234000
fl_lab_peer_icmp_reachable{observer_id="observer",node_id="target"} 1 1234000
fl_lab_peer_icmp_packet_loss_percent{observer_id="observer",node_id="target"} 0 1234000
fl_lab_peer_icmp_rtt_seconds{observer_id="observer",node_id="target"} 0.075 1234000
`
	if output.String() != want {
		t.Fatalf("unexpected sample\n%s", output.String())
	}
}
