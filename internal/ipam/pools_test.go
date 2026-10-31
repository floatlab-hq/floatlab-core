package ipam

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func TestConcurrentNodeRangesRemainDisjoint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "lab.sqlite")
	first, err := rqlite.OpenLocal(path, "lab")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err = rqlite.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	second, err := rqlite.OpenLocal(path, "lab")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	pools := []Pool{
		{Name: "a", CIDR: "192.0.2.0/24", StartIP: "192.0.2.10", EndIP: "192.0.2.20", NodeIDs: []string{"a"}},
		{Name: "b", CIDR: "192.0.2.0/24", StartIP: "192.0.2.20", EndIP: "192.0.2.30", NodeIDs: []string{"b"}},
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	errors := make(chan error, 2)
	for i, db := range []*rqlite.Client{first, second} {
		go func() {
			<-start
			if err := SaveNodePool(ctx, db, &pools[i], pools[i].NodeIDs[0]); err != nil {
				errors <- err
				results <- -1
			} else {
				results <- i
			}
		}()
	}
	close(start)
	winner, other := <-results, <-results
	if winner < 0 {
		winner, other = other, winner
	}
	if winner < 0 || other != -1 {
		t.Fatalf("expected exactly one committed range: %d, %d", winner, other)
	}
	if err = <-errors; !strings.Contains(err.Error(), "reserved node range") {
		t.Fatalf("loser failed without enforcing the range constraint: %v", err)
	}
	stored, err := ListPools(ctx, first)
	if err != nil || len(stored) != 1 || stored[0].ID != pools[winner].ID {
		t.Fatalf("failed reservation partially wrote its pool: %v, %v", stored, err)
	}
	if err = DeletePool(ctx, first, pools[winner].ID); err != nil {
		t.Fatal(err)
	}
	loser := &pools[1-winner]
	if err = SaveNodePool(ctx, second, loser, loser.NodeIDs[0]); err == nil {
		t.Fatal("deleting a pool released its reserved node range")
	}
	changed := pools[winner]
	changed.NodeIDs = []string{"replacement"}
	if err = SaveNodePool(ctx, second, &changed, "replacement"); err == nil {
		t.Fatal("deleted pool reservation changed owners")
	}
	legacy := Pool{Name: "legacy", CIDR: "192.0.2.0/24", StartIP: "192.0.2.100", EndIP: "192.0.2.110", NodeIDs: []string{"legacy"}}
	if err = SavePool(ctx, first, &legacy); err != nil {
		t.Fatal(err)
	}
	if err = config.Migrate(ctx, first); err != nil {
		t.Fatal(err)
	}
	if err = DeletePool(ctx, first, legacy.ID); err != nil {
		t.Fatal(err)
	}
	legacy.ID, legacy.Name, legacy.NodeIDs = "", "reuse", []string{"replacement"}
	if err = SaveNodePool(ctx, second, &legacy, "replacement"); err == nil {
		t.Fatal("migration did not retain a legacy pool's range after deletion")
	}
}

func TestValidatePool(t *testing.T) {
	valid := Pool{Name: "apps", CIDR: "192.0.2.0/24", StartIP: "192.0.2.10", EndIP: "192.0.2.20"}
	if err := ValidatePool(valid); err != nil {
		t.Fatal(err)
	}
	valid.StartIP = "192.0.2.0"
	if err := ValidatePool(valid); err == nil {
		t.Fatal("network address accepted")
	}
}

func TestPoolMembership(t *testing.T) {
	for _, nodes := range [][]string{nil, {""}, {"node", "node"}} {
		if ValidateMembers(nodes) == nil {
			t.Fatal("invalid member list accepted", nodes)
		}
	}
	pool := Pool{NodeIDs: []string{"a", "b"}, MembershipResolved: true}
	if !Member(pool, "a") || Member(pool, "c") {
		t.Fatal("explicit membership not enforced")
	}
	pool.MembershipResolved = false
	if Member(pool, "a") {
		t.Fatal("unresolved migration eligible")
	}
	pool = Pool{StartIP: "192.0.2.10", EndIP: "192.0.2.20"}
	if outsideRange([]string{"192.0.2.10/24"}, pool) || !outsideRange([]string{"192.0.2.2/24"}, pool) {
		t.Fatal("host address reservation failed")
	}
}

func TestNamedAndHostDefaultSelection(t *testing.T) {
	pools := []Pool{{ID: "shared", Name: "lan", NodeIDs: []string{"a", "b"}, MembershipResolved: true}, {ID: "local", Name: "local", NodeIDs: []string{"a"}, MembershipResolved: true}}
	pool, err := SelectPool(pools, "", "shared", "b")
	if err != nil || pool.ID != "shared" {
		t.Fatal("host default failed", err)
	}
	pool, err = SelectPool(pools, "local", "shared", "a")
	if err != nil || pool.ID != "local" {
		t.Fatal("named pool did not override default", err)
	}
	if _, err = SelectPool(pools, "local", "shared", "b"); err == nil {
		t.Fatal("host-bound pool selected on another host")
	}
	if _, err = SelectPool(pools, "", "", "a"); err == nil {
		t.Fatal("pool implicitly selected without a host default")
	}
}
