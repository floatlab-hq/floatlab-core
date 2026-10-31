package control

import (
	"context"
	"net/http"
	"testing"

	"github.com/floatlab/floatlab-core/internal/ipam"
)

func TestPeerModeCannotReassignOrDeleteAnotherNodesRange(t *testing.T) {
	s, db := peerWorkloadServer(t, "node-a")
	pool := ipam.Pool{Name: "reserved", CIDR: "192.0.2.0/24", StartIP: "192.0.2.100", EndIP: "192.0.2.120", NodeIDs: []string{"node-b"}}
	if err := ipam.SavePool(context.Background(), db, &pool); err != nil {
		t.Fatal(err)
	}
	pool.NodeIDs = []string{"node-a"}
	if err := s.validateLocalPool(context.Background(), pool); err == nil {
		t.Fatal("implicitly transferred a remote owner's address reservations")
	}
	response := peerRequest(t, s, http.MethodDelete, "/api/v1/settings/network-pools/"+pool.ID, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("deleted remote range: %d %s", response.Code, response.Body.String())
	}
	remaining, err := ipam.ListPools(context.Background(), db)
	if err != nil || len(remaining) != 1 || remaining[0].NodeIDs[0] != "node-b" {
		t.Fatalf("remote reservation changed: %+v %v", remaining, err)
	}
}

func TestPeerModeRejectsUnscopedIPv6Allocation(t *testing.T) {
	s, _ := peerWorkloadServer(t, "node-a")
	if err := s.SetWorkloadPeers(t.TempDir(), &http.Client{}, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	response := peerRequest(t, s, http.MethodPost, "/api/v1/network/allocations", `{"stack_id":"web","service":"app","prefix_pool":"shared"}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("allocated an address without a node-owned range: %d %s", response.Code, response.Body.String())
	}
}
