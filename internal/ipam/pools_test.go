package ipam

import "testing"

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
