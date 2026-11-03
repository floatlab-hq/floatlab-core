# Multi-node lab operations

Implementation companion to [the multi-node plan](MULTI_NODE_LAB_PLAN.md).
Use a private LAN or an existing VPN between the appliances. Each appliance
keeps its existing unique persistent node identity; copying an appliance's
identity or workload disk does not create a new node.

## Enable peer transport

On each appliance, set these literal environment values for `floatlab-control`
in `/floatlab/system/docker-compose.yml`, using that appliance's reachable
private IP:

```yaml
environment:
  FLOATLAB_PEER_URL: https://192.168.50.11:9443
  FLOATLAB_RQLITE_RAFT_ADDRESS: 192.168.50.11:4002
  FLOATLAB_RQLITE_ADDRESS: 192.168.50.11:4001
```

Published release Compose files have resolved interpolation already: changing
only `.env` will not change a literal value in that file. Keep the existing
identity, bootstrap and credential environment settings. Recreate only the
management controller after changing its environment:

```sh
docker compose -p floatlab-system -f /floatlab/system/docker-compose.yml up -d --no-deps floatlab-control
```

Initialization configures the pinned rqlite sidecar's TLS and advertised
addresses. Allow peer TCP 9443, 4001, 4002 and restricted SSH TCP 22 over the
private network. Consensus ingress permits enrolled node addresses; the peer API
requires lab certificates except for invitation-authenticated enrollment.
The SSH replication identity has a forced receiver command and cannot open a shell
or forward connections. Do not expose these ports through public NAT.

## Join an empty appliance

Sign in to the source lab's CLI or browser command palette and run `lab invite`.
The returned token expires in 15 minutes and is bound to one enrolling identity.
Keep it private. On the empty appliance, use its current standalone administrator
login and run:

```text
lab join '{"invitation":{...invitation response...},"peer_url":"https://192.168.50.12:9443","raft_address":"192.168.50.12:4002","rqlite_address":"192.168.50.12:4001"}'
```

The joining appliance must have no workloads, retained recovery points, orphaned
workload datasets or protection material. Joining replaces its standalone lab
configuration after a verified backup. It retains its node identity and becomes
a non-voter before promotion to a voter. Retry an interrupted join with the same
invitation and endpoints. After joining, sign in with the source lab's credentials.
Use `lab status` and `lab members` to inspect membership, quorum, trust revisions
and workload replica revisions.

Assign a separate non-overlapping published service address range to each node
through `/api/v1/settings/network-pools` while quorum is healthy. A pool must
belong exclusively to the node submitting it. Unassigned or exhausted ranges fail
allocation. The system does not borrow an unreachable node's addresses.
The legacy manual IPv6 prefix-allocation endpoint is rejected in peer mode:
those prefixes have no node ownership. Node-assigned published service pools
use the existing IPv4 allocation path. IPv6 node-scoped ranges require a separate
extension before that legacy endpoint can safely operate across nodes.

## Partitions and remote stacks

Connect directly to each reachable appliance's normal API, CLI or browser.
Local create, edit, start, stop and delete use its local SQLite database. Shared
configuration writes require rqlite quorum: both voters in a two-node lab, or two
of three voters in a three-node lab. Locally cached users and security policy may
be stale during a partition; token and certificate expiry still apply.
Offline starts reuse cached images. Missing images, explicit `pull_policy: always`
and fresh-image upgrade requests still require registry connectivity; builds need
their base images and build inputs available locally.

The stack list includes read-only peer views with `replica`, `stale`,
`observed_generation`, `observed_revision` and `observed_at`. A cached view does
not prove that its owner is reachable. Mutations are forwarded to the owner over
authenticated peer transport; an unreachable owner returns
`workload_owner_unavailable`. The receiver never executes jobs from a peer copy.
When display names collide, select `node-name/stack-name` in the CLI or command
palette. Database replication catches up after reconnecting without polling
workload revisions. Database replication does not copy application data or images.

## Remove or recover a member

With quorum, run `lab preview NODE` to inspect cached workloads, recovery points,
reserved ranges and warnings, then `lab remove NODE OPERATION-ID`. Reuse the
operation ID for retries. Removal preserves workload data and peer copies and
revokes peer/SSH access; it does not stop an unreachable workload or release its
addresses. The old node identity cannot enroll again.

Without quorum, ordinary removal fails. Fence every excluded node's lab consensus
service independently, then run the explicit root host command on the survivor:

```sh
floatlab-installer-hostd lab-recover --operation-id recovery-001 --survivor NODE-ID --exclude EXCLUDED-ID --confirm-fenced
```

For multiple excluded nodes, pass a comma-separated list. A failed ping is not
fencing. The command keeps checksum-verified backups and a recovery journal,
rebuilds only rqlite membership, and retains tombstones and address reservations.
Repeat the same command to resume. Inspect recovered configuration before
resuming shared changes; the survivor may not have the last committed change.
Workload takeover is a separate fenced recovery operation under the protection
plan and is not authorized by lab recovery.

Reset a removed appliance to an empty installation with a new node identity
before enrolling again. If its peer management IP must be reused, explicitly run
`lab release-peer-address OLD-NODE OPERATION-ID --confirm-fenced --confirm-reset`.
This releases only the management peer IP quarantine; workload ranges stay reserved.

`lab rotate OPERATION-ID` rotates the local leaf certificate and database publisher
key. Retry the same operation until all active nodes acknowledge the new trust
revision. Rotation does not silently discard disconnected nodes or rotate the lab
CA or host SSH key.

## Automated verification

The Go integration suite builds appliances, creates owned libvirt fixtures,
partitions guest interfaces, runs local API requests through the guest agent,
checks actual published HTTP service access and compares local/replica SQLite
state after healing. It includes joining a populated source, two- and three-node
partitions, controller restart, normal removal and fenced lost-quorum recovery.
It cleans up its VMs, private network and storage pool automatically.

Run inside the development shell on a host with KVM and libvirt access:

```sh
FLOATLAB_INTEGRATION=1 go test -count=1 -timeout 12h -v ./integration \
  -args -ginkgo.label-filter=multi-node
```

Each appliance uses 3584 MiB RAM. Reports are written to a unique
`/tmp/floatlab-reports-*` directory; `FLOATLAB_REPORT_DIR` changes the parent
directory. Omit the label filter to include single-node workflows, package
integration and host upgrades. Existing appliances are not supported. Run the
full suite twice when qualifying repeatability.
