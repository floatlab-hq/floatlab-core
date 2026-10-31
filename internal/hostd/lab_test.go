package hostd

import (
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/internal/lab"
)

func TestLabFirewallPreservesRemovalQuorumUntilMembershipCommits(t *testing.T) {
	members := []lab.Member{{NodeID: "owner", Status: "active", RaftAddress: "192.0.2.1:4002"}, {NodeID: "target", Status: "removing", RaftAddress: "192.0.2.2:4002"}}
	rules, err := labFirewallRules("owner", members, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rules, `ip saddr { 192.0.2.2 } tcp dport { 22, 9443 } reject`) {
		t.Fatal("removing node must lose new peer/SSH sessions", rules)
	}
	if strings.Contains(rules, `ip saddr { 192.0.2.2 } tcp dport { 22, 4001, 4002, 9443 } reject`) {
		t.Fatal("revoking consensus before membership commit loses two-node quorum")
	}
	if !strings.Contains(rules, `ip saddr != { 192.0.2.1, 192.0.2.2 } drop`) {
		t.Fatal("removing node must remain in consensus source allowlist", rules)
	}
	members[1].Status = "removed"
	rules, err = labFirewallRules("owner", members, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rules, `ip saddr { 192.0.2.2 } tcp dport { 22, 4001, 4002, 9443 } reject`) {
		t.Fatal("committed removal must revoke consensus as well", rules)
	}
	members[1].PeerAddressReleased = true
	rules, err = labFirewallRules("owner", members, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rules, `saddr { 192.0.2.2 }`) {
		t.Fatal("explicit reset address release left tombstone IP blocked", rules)
	}
}

func TestLabFirewallRejectsUnenrolledConsensusBeforeDockerDNAT(t *testing.T) {
	rules, err := labFirewallRules("owner", []lab.Member{{NodeID: "owner", Status: "active", RaftAddress: "192.0.2.1:4002"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rules, `hook prerouting priority -110`) || !strings.Contains(rules, `ip daddr 192.0.2.1 tcp dport { 4001, 4002 } ip saddr != { 192.0.2.1 } drop`) {
		t.Fatal("consensus could bypass node allowlist via Docker publishing", rules)
	}
}

func TestLabEmptinessRejectsOrphanedWorkloadAndProtectionMaterial(t *testing.T) {
	names := []string{"floatlab", "floatlab/os", "floatlab/os/v1@base", "floatlab/system", "floatlab/system/docker/a", "floatlab/stacks", "floatlab/orphan@retained", "floatlab/stacks/old", "floatlab/system/protection/old/stack"}
	retained := labRetainedDatasets(names)
	if len(retained) != 3 || retained[0] != "floatlab/orphan@retained" || retained[2] != "floatlab/system/protection/old/stack" {
		t.Fatal(retained)
	}
}

func TestRecoveryQuarantineCannotBeOverwrittenByMembershipRefresh(t *testing.T) {
	rules, err := labRecoveryFirewallRules("enp1s0", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"delete table inet floatlab_lab_recovery", "hook prerouting priority -120", `iifname "enp1s0" tcp dport { 4001, 4002, 9443 } drop`} {
		if !strings.Contains(rules, want) {
			t.Fatal("missing quarantine rule", want, rules)
		}
	}
	ordinary, err := labFirewallRules("owner", []lab.Member{{NodeID: "owner", Status: "active", RaftAddress: "192.0.2.1:4002"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ordinary, "floatlab_lab_recovery") {
		t.Fatal("ordinary trust refresh can change recovery quarantine", ordinary)
	}
	if _, err := labRecoveryFirewallRules("eth0\" accept;", false); err == nil {
		t.Fatal("accepted injected nft interface")
	}
}
