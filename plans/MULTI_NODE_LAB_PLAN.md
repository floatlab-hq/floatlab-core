# Multiple Nodes in One Lab

Status: implemented; requested appliance acceptance passed. Updated 2026-10-31.

The working tree now contains the storage split, local lifecycle authority,
cached lab authentication, disjoint range validation, enrollment and private
transports, native SQLite database replication, owner forwarding, removal and
fenced lab recovery. API, CLI and the browser command palette expose the lab
operations. See [the operations guide](MULTI_NODE_LAB_OPERATIONS.md) for setup,
commands and automated test entry points.

Worker Go race tests, native synchronization tests, client/CLI/UI tests and
TypeScript checks have passed. The fresh-appliance multi-node suite passed all
five scenarios, including partition recovery, explicit fencing and independent
verification of replica contents. The validation record below distinguishes
these results from remaining validation gaps. Snapshot/data protection, clone
and workload takeover remain owned by the separate protection plan. Legacy
cross-node takeover is blocked in peer mode until that fenced workflow is implemented.

## Goal and relationship to the protection plan

Allow an empty FloatLab appliance to join a lab while each node can create,
start, stop, edit, and recover its own workloads when the lab is partitioned or
has no rqlite quorum. Local operation is the normal execution model, not a
separate emergency mode.

This plan owns membership, node identity, transport, shared configuration,
node-local workload databases, and their read-only replication. The
[stack protection plan](STACK_PROTECTION_PLAN.md) owns workload snapshots,
data/image replication, retention, clones, restoration, and takeover.

This revision supersedes the earlier requirement that all workload operations
need quorum in rqlite and application Raft. Keep rqlite for shared lab configuration;
move workload authority and state into node-local SQLite. Replace application
Raft-backed workload transitions with local transactional transitions rather than
retaining a hidden distributed quorum dependency.

## State ownership and persistence

| State | Authoritative store | Replication and writes |
| --- | --- | --- |
| Lab identity, members, trust, users/roles, shared policy, network definitions and node address-range assignments | Lab-wide rqlite | Consensus replication; mutations require quorum |
| Stack identity/owner, Compose and workload configuration, desired/observed lifecycle state, protection policies, allocations within assigned ranges | Owner node SQLite | Local writes; asynchronous read-only copies on every other node |
| Workload tasks, operation checkpoints, locks, idempotency records, lifecycle events, workload alerts/notifications, snapshot/recovery metadata, scheduler progress, destination acknowledgements | Owner node SQLite | Local writes; included in that node's read-only replicas |
| Destination receive jobs, local backup inventory, local retention pins, recovery journal and locally created clones | Destination node SQLite | Destination-local writes; copied out as its own node state |
| Other nodes' workload state | Separate read-only SQLite copies | Written only by verified replication from the source owner |
| Metrics, logs, workload data and container images | Existing telemetry, ZFS and Docker storage | Separate from database replication |

Lab-wide operations/alerts remain in rqlite; workload-scoped records move to local
SQLite. A destination's receive acknowledgement is an authenticated message to
the source, which records it in its own database; no node writes another node's
replica or merges two databases. Track canonical project files and required
secret artifacts by revision/hash alongside the database copies. Large workload
data and image content travel only through explicit protection policies.

Use `floatlab/system/workloads` mounted at `/floatlab/system/workloads` for the
local database, private cached lab configuration and peer replicas. The writable
database is `local.sqlite`; read-only peer files are
`replicas/<source-node-id>.sqlite`. Keep this dataset outside management-update
rollback trees so reverting management does not rewind workload ownership or
operation state. Software rollback must reject an incompatible local schema.

## Decisions and boundaries

- One lab with shared configuration; no merging populated labs.
- Connectivity comes from a LAN or existing VPN. Built-in tunnels, discovery,
  NAT traversal, relays and cross-site workload IP mobility are deferred.
- Only an empty server joins. Reject workloads, retained workload recovery
  material, or existing membership in another multi-node lab. Replace standalone
  configuration only through the explicit join transaction.
- Each node is the sole writer and normal executor for its workloads. Read-only
  replicas provide visibility and recovery material, not execution authority.
- All nodes replicate lab configuration through rqlite and publish their own
  workload database one way to every other enrolled node.
- A two-voter rqlite lab needs both members for shared writes; a three-voter lab
  can tolerate one failure. Never shrink membership automatically for quorum.
- Rqlite unavailability never blocks local workload commits or makes them wait
  for peer database acknowledgement. Remote views are explicitly eventually
  consistent and can be stale.

## Enrollment and authenticated communication

An administrator creates a single-use join invitation valid for 15 minutes,
identifying the lab, bootstrap address and pinned certificate fingerprint.
The joining server generates its own private key and proves possession during
enrollment. Peer APIs and protection streams subsequently use mutual TLS and
lab-issued certificates. Workload database replication uses restricted SSH with
`sqlite3_rsync`, as specified below. Never copy another node's private key or
host identity.

Every enrolled appliance is trusted to administer lab membership. Distribute the
lab CA signing key only within the pinned, encrypted enrollment response and keep
it in each appliance's root-only lab identity directory. Never put it in invitation
records or read-only workload replicas; scrub transient join journals on completion.
This shared signing authority lets a surviving member enroll replacements after
the original node is removed. Per-node leaf and replication private keys remain
unique and local. Compromise of any enrolled administrator appliance therefore
compromises the lab signing authority and requires explicit lab trust rotation.

Enrollment is a durable, retryable transaction:

1. Validate invitation, connectivity, software compatibility and server emptiness.
2. Provision the new identity, lab credentials and restricted database-replication
   SSH access, preserving standalone rollback information before replacement.
3. Initialize the node's writable workload database and receive current lab
   configuration plus read-only workload views from existing reachable members.
4. Join rqlite as a non-voter, verify catch-up, and then promote it.
5. Commit membership, node address ranges and approved peer access before enabling
   normal provisioning. Report missing peer copies and complete their sync later;
   an unavailable workload-view source must not masquerade as an empty source.

Journal partial enrollment and reconcile it before further membership changes.
There is no second application Raft voter set in the target architecture.

Retain local Unix-socket hostd access. Add mutually authenticated peer endpoints
for resource-scoped host operations, replica status and protection streams.
Encrypt every cross-host channel, including rqlite consensus and configuration
transfer; a TLS management endpoint alone is insufficient. Bind peer listeners
only to configured peer interfaces and restrict ingress with host firewall rules.
Never expose unrestricted IPC, arbitrary shell commands or arbitrary paths.

Verify lab/node identity, allowed resources and cached enrollment state for every
peer request. Support certificate rotation/revocation and refuse removed peers
as soon as a newer committed trust revision is learned. A disconnected node cannot
learn a new revocation immediately; cached authorization is discussed below.

## One-way workload database replication

Each node publishes its own database to every other enrolled node asynchronously.
Use SQLite's official `sqlite3_rsync` utility to synchronize consistent live
database snapshots while transferring differing pages. This is a separate bundled
executable, not ordinary filesystem rsync or a continuously running SQLite
replication service. Do not build a custom SQL change-log replay engine or a
whole-database transfer protocol over mutual TLS.

Bundle the same pinned, tested `sqlite3_rsync` release on both ends, version
3.50.0 or newer, using an explicit remote executable path on the appliance.
Verify utility/protocol compatibility during enrollment and software preflight;
do not silently use ordinary rsync or a mismatched older protocol.

### Commit-driven dispatch

Persist a source node ID, database generation and monotonically increasing
revision in the database; increment the revision in each workload-state write
transaction. Signal a replication worker only after commit. Route every workload
write through this transaction/notification path; rollbacks never signal success.
There is no periodic database polling loop.

Debounce notifications for 250 ms, with a maximum one-second delay under sustained
writes, then synchronize lagging peers. Serialize work per source/destination and
coalesce pending database-view revisions; this does not coalesce retained ZFS
recovery points. Writes during a synchronization request a subsequent run.
Replication remains asynchronous and never delays a local commit on peer access.

Persist per-peer applied generation/revision and synchronization status in private
local replication bookkeeping outside the replicated workload database. Updating
these acknowledgements must not increment workload revisions or create an endless
replication feedback loop.

At publisher startup and peer reconnection, compare current source revision with
durable acknowledgements and synchronize lagging peers. These checks cover a
crash after commit but before notification. Retry failed connections/transfers
with capped exponential backoff (one second up to one minute), reset on success;
peer reconnection can wake the worker immediately. Retry timers and connection
liveness handling are not periodic database polling. Re-read the revision after
a run so notifications coalesced while busy cannot leave a trailing commit unsent.

### Restricted SSH and safe replica publication

During authenticated enrollment, exchange/pin node SSH host keys and provision a
separate per-source replication key on each destination. Keep private keys local.
Use a dedicated unprivileged database-replication account, strict host-key
verification and non-interactive authentication. Never disable host-key checking
or reuse a root/admin SSH key.

Authorized keys invoke a fixed receiver wrapper with shell, forwarding, agent,
X11 and PTY access disabled. The wrapper maps the authenticated source to its
replica directory, validates the permitted `sqlite3_rsync` receiver invocation,
and rejects arbitrary commands, paths and attempts to write another node's
replica or the destination's writable database. Apply the peer-interface/firewall
restrictions to SSH as well. Rotate/revoke these keys with enrolled membership;
host-key changes require authenticated rotation, never trust-on-first-use retry.

For each run, prepare a private staging database on the destination. Seed it from
the last published replica with a local SQLite-consistent copy when available,
so remote synchronization transfers differing pages rather than starting empty
each time. Run `sqlite3_rsync` from the live origin into that staging database;
keep the currently published replica read-only and untouched during transfer.

After successful synchronization, validate integrity, source identity, schema
and database generation/revision from the received database itself. A transfer
may capture a later revision than the triggering notification; acknowledge only
the revision actually received. Normalize/checkpoint staging into a standalone
closed database before atomic publication, with no required WAL sidecars. Do not
require byte-identical database file hashes: the utility may change SQLite header
metadata while preserving table/index contents.

Atomically replace the prior replica and durably record the applied revision
before returning acknowledgement. Open published replicas read-only; existing
readers may finish against the previous file. Reject older revisions and preserve
the last good published replica on interrupted, invalid or failed runs. Retrying
a completed transfer/publication is idempotent even if its acknowledgement was lost.

On reconnection, synchronize the latest consistent view without merging databases
or replaying obsolete intermediate snapshots. A different database generation
requires an explicit source restore/rebuild transition; never silently accept a
stale source or promote a replica. Restoring an owner database from a peer is an
explicit recovery action requiring fencing of any old owner.

Replicas never run migrations, tasks, schedulers or alerts for their source node.
Show source node, generation, revision, synchronization time and stale/unreachable
status in aggregate views. A replica snapshot is not a copy of its ZFS data or
images, and is not guaranteed to contain the latest local commit before failure.

References: [SQLite remote-copy utility](https://sqlite.org/rsync.html) and
[SQLite online backup API](https://sqlite.org/backup.html) for local staging copies.

## Local operation during quorum loss

The normal API, CLI and UI continue to create, start, stop, edit, upgrade, snapshot,
restore and clone locally owned workloads using local SQLite. Local restart
reconciliation uses durable owner state and journals; it does not query a lab
leader before starting an already locally owned workload.

Preserve the earlier no-automated-failover rule: lab quorum loss blocks shared
configuration changes and automatic cross-node takeover, not local execution.
Local workload scheduling, health/restart handling, snapshot capture and safe
retention continue. Previously authorized transfers may proceed between reachable
peers using cached configuration and durable jobs. Unreachable destinations accrue
backlog under the protection plan rather than making local work fail.

Use globally unique stack IDs and node-qualified lookup names; duplicate display
names across nodes require explicit node selection. Forward mutations to the
owner when reachable; never apply them to a cached remote database. Return a clear
owner-unavailable result when it is not reachable.

Allocate IPs transactionally in local SQLite from a disjoint address range assigned
to the node in rqlite beforehand. Never reassign a range from an unreachable node.
New ranges, shared pool changes and range transfers require quorum. If a node has
no eligible addresses, reject published networking for a new stack rather than
allocating from an unassigned range; isolated/private workloads can still run.

Persist the last applied lab configuration and authorization material locally,
with its revision. Normal local authentication/authorization uses that committed
cache when rqlite reads are unavailable; local authentication counters are local
SQLite writes. Continue enforcing token/certificate expiry and explicit validity
limits. Do not bypass authentication because quorum is lost. Security updates
cannot take effect on disconnected nodes until delivered; display the stale
configuration revision. Root hostd recovery remains available independently.

Lab-wide user/role changes, membership, trust changes, shared network changes and
new cross-node ownership handoffs require rqlite quorum. Report accepted and
per-node applied configuration revisions separately; a committed change is not
proof that every disconnected node has applied it.

## Ownership, takeover and reconnection

Ordinary operation always has one owner node. Automatic takeover while another
owner is unreachable is forbidden. A quorum-backed migration requires the old
owner to durably relinquish execution authority before the new owner starts.

Manual production takeover without quorum requires explicit fencing and a local
recovery journal/ownership generation as defined in the protection plan. Read-only
replicas and a failed ping never grant ownership. Reconnection surfaces ownership
conflicts and blocks automatic actions for the affected stack until an
administrator selects the authoritative instance; unrelated local stacks continue.

Routine disconnected edits to each node's own stacks do not create conflicts to
merge: their owner databases remain authoritative, and refreshed replicas replace
stale views. Never automatically merge divergent application data or fail back.

## Unavailable-node removal and lost-quorum recovery

Node health is observational, not permission to evict a member. Never remove a
node or reduce voting membership automatically because of failed heartbeats.
Keep automatic rqlite voter reaping and removal-on-shutdown disabled. Separate
ordinary removal with quorum from explicit recovery of a lab that has lost quorum.

### Explicit removal while quorum remains

An administrator can remove an unavailable member when the surviving rqlite
cluster can commit membership changes, for example two healthy voters out of
three. The unavailable member need not acknowledge removal. Reject removing the
last voter through this operation, or any request that cannot complete through
the existing quorum; direct the administrator to recovery instead.

Provide a removal preview showing the node's last known workloads, backup
destinations, retained database view, synchronization age, credentials, address
ranges and resulting voting membership. Stale remote workload data must be
labelled as such. Require explicit confirmation of the affected resources; this
operation does not claim that unavailable workloads have stopped.

Journal the transaction before changing membership, with a durable removal intent
and a final removed-node tombstone rather than deleting the node's identity:

1. Mark the node as removing, block new assignments/peer sessions, and durably
   record the intended membership change and retained resources in rqlite.
2. Revoke its lab certificate authorization and database-replication SSH access
   on reachable members. Track per-member application of the trust revision;
   disconnected members enforce it on reconnection before accepting peer work.
3. Remove its rqlite voting/non-voting membership through the supported leader
   interface. Keep the lab removal record distinct from rqlite's peer list and
   reconcile partial completion after interruption.
4. Stop dispatch and new database/protection transfers involving that member,
   cancel connection retries, and mark its workloads unavailable or ownership
   unresolved. Preserve their last observed state for inspection.
5. Complete the tombstone, recording actor, operation ID, removed identity,
   membership outcome, revocation progress and outstanding workload disposition.

Make removal resumable/idempotent after every step. A committed membership change
with incomplete local cleanup remains visible and retryable; do not report that
every member applied revocation merely because the intent committed.

Preserve the last good read-only workload database, project artifacts and all
existing recovery points/images. Ordinary removal does not purge data or start
the removed node's workloads elsewhere. Mark backup policies targeting it as
blocked and retain pending history; replacing/retiring that destination and
releasing its dependencies require a separate explicit policy change under the
protection plan. Removing a node must not silently pretend unsent points synced.

Quarantine its assigned IP ranges and other exclusive resources. Reuse requires
verified fencing plus explicit workload/resource disposition and a quorum-backed
configuration change. Membership removal and credential revocation cannot stop
containers or IP use on a partitioned machine.

### Explicit lab recovery after quorum is lost

With one survivor out of two voters, ordinary removal cannot commit. Continue
normal local SQLite workload operations, but expose a separate root recovery CLI
workflow for rebuilding lab-configuration consensus. Do not add a force-delete
flag to the ordinary node API or silently turn a survivor into a new leader.

For the first implementation, recover one explicitly selected surviving rqlite
member, then enroll additional empty nodes through the normal join flow:

1. Show the old membership, selected survivor, available configuration revision,
   excluded nodes, and uncertainty about the latest lab-wide changes.
2. Require administrator confirmation that every excluded old member is fenced
   from the recovered lab and cannot independently rejoin or serve a competing
   lab configuration. A failed ping does not satisfy fencing. Workload takeover
   or IP reuse additionally requires fencing the old workload execution/network.
3. Stop rqlite on the selected survivor and make a verified backup of its full
   data directory, configuration and recovery journal. Do not stop or rewind the
   survivor's local workload database, containers or ordinary local operations.
4. Use the pinned rqlite version's supported lost-quorum membership-recovery
   procedure to form the selected survivor's single-voter lab. Journal each
   checkpoint outside the rqlite rollback tree and reject concurrent recovery.
5. Verify lab database readiness and inspect recovered configuration. Explain
   that lost-quorum recovery can commit formerly outstanding log entries or lose
   unavailable state; do not promise the exact last globally acknowledged state.
6. Before opening peer access, commit the excluded-node tombstones and trust/SSH
   revocations, preserve their resource reservations, and record the recovery
   outcome. Resume shared administration only after these checks succeed.

An interrupted recovery stays explicit and retryable, with original backups
retained. It must never bootstrap each side of a partition automatically. Preserve
all workload replicas/backups for separate recovery, and do not merge recovered
lab databases or silently run imported peer tasks.

Reference: [rqlite lost-quorum recovery](https://rqlite.io/docs/clustering/general-guidelines/#recovering-a-cluster-that-has-permanently-lost-quorum).
Validate the procedure against the appliance's pinned rqlite release rather than
assuming current upstream file paths or flags match it.

### Returning removed nodes

Refuse old TLS/SSH credentials, replication uploads, API mutations and rqlite
rejoining from tombstoned identities. Keep tombstones through backup/restore and
normal membership changes; never recycle removed node IDs.

A removed server must remain isolated while its old workload/configuration state
is inspected and deliberately migrated, exported or cleared. Require explicit
reset to an empty appliance, fresh node identity and new credentials before the
normal enrollment flow accepts it. Never auto-wipe the returning server, merge
its local workload database, or resume old stack execution on peers. Historical
backups continue to reference its original node identity.

## Migration, interfaces and implementation sequence

Current code stores workload rows in rqlite and lifecycle state in application
Raft. Split those repositories and controllers together; changing only the database
adapter would leave quorum dependencies in startup, dispatch, authentication and
network allocation.

For existing standalone installs, pause workload mutations/dispatch, make verified
backups, capture consistent workload rows plus application lifecycle state, import
into local SQLite, verify counts/identities/checkpoints, and durably switch the
workload store before resuming. Preserve original records for recovery but make
one authority active. Never migrate a peer's workload rows into local ownership.
Include local SQLite in management preflight/schema compatibility checks while
preserving it through ordinary management snapshot rollback.

Reuse administrator authentication, idempotency keys and asynchronous operation
IDs, now persisted in the correct local or lab store. Keep existing standalone-only
update restrictions until coordinated multi-node updates are designed.

Extend node management with removal preview, explicit removal and resumable status
using the normal administrator/idempotency conventions. The current delete-node
handler only deletes a configuration record after checking stack assignments;
replace that behavior with the membership/removal transaction, preserving the
unavailable-node workload and backup inventory instead of silently deleting it.
Expose lost-quorum lab recovery only through the separately confirmed root CLI
workflow. Show revoked/pending members, reserved resources and required re-enrollment.

Implementation order:

1. Split lab and workload storage; migrate standalone workload rows/state and remove
   application Raft gates from local operations.
2. Add cached configuration/authentication, node-scoped names and preassigned local
   allocation ranges so local API/CLI/UI work without rqlite quorum.
3. Add identity, invitation validation, mutual TLS, restricted replication SSH,
   enrollment and rqlite membership; bundle/pin the synchronization utility.
4. Add commit-driven `sqlite3_rsync`, validated replica publication, durable peer
   acknowledgements, startup/reconnect catch-up and stale views.
5. Add explicit removal, tombstones/revocation, lost-quorum lab recovery and safe
   re-enrollment, then ownership handoff/recovery integration and appliance acceptance.

## Validation record — 2026-10-31

`scripts/test-multi-node.sh` completed successfully against fresh disposable
libvirt appliances built from the frozen implementation. `TestMultiNodeLab`
passed in 599.84 seconds with no skipped scenarios or diagnostic binary patches.
Every VM used this same ISO:

```text
/nix/store/nj8rk2dmlqs8k70x8650ig1whmlfijdn-floatlab-installer-unknown.iso/iso/floatlab-installer-unknown.iso
```

The bundled control image was `sha256:741e4c2e546c9032e20112038157b0d9d435cc7b875277b13382acae81c4d21c`,
also independently verified inside the running appliance. This final build includes
the joined-node bootstrap credential guard and the host daemon's SQLite tool path.
The appliances used pinned rqlite `8.26.0`, image
`rqlite/rqlite:8.26.0@sha256:b81e945809e1fcc92f3d72c07d7f8726252dfb5ed406db6faaf6879159193fe9`,
and the bundled static SQLite/`sqlite3_rsync` tools `3.50.4`, built from
checksum-verified sources by `docker/build-sqlite-tools.sh`. The artifact was
built from the frozen working tree before its implementation commit; the image
digest and ISO store path identify the tested build rather than a release tag.
Run log: `/tmp/floatlab-multi-node-final-nj8rk2/multi-node.log`.

| Automated appliance scenario | Result |
| --- | --- |
| Populated one-node lab with published web service enrolls an empty node; retire its distinct standalone account; owner forwarding, aggregate views, native database replication and joined-node certificate rotation | Passed, 30.47 s |
| Two-node partition: both nodes create/start/stop/delete services within preassigned disjoint ranges; isolated controller restart preserves lab authentication/state and rejects old standalone credentials; local lifecycle also works with its rqlite process stopped; heal and compare replicas | Passed, 114.94 s |
| Three-node partition: majority changes shared configuration, all nodes manage local services, minority receives the new configuration and all workload replicas after reconnection | Passed, 113.52 s |
| Remove unavailable third node with quorum: preview stale workload inventory, preserve its replica, and reject its old peer/rqlite and restricted SSH credentials after return | Passed, 10.83 s |
| Two-node lost quorum: reject ordinary removal and recovery without fencing confirmation; explicitly fenced recovery restores shared writes, retains verified backup and existing service, supports idempotent retry, and rejects the returned old TLS/SSH identity and populated rejoin | Passed, 35.85 s |

Service checks fetched the expected content through published HTTP ports, rather
than relying solely on API lifecycle state. Catch-up checks independently queried
source and received SQLite files for node identity, generation, revision, stacks,
lifecycle, operations, runtime, allocations and events, including deletions.
Peer replicas never produced duplicate source-owned containers.

The test's domains, disks, transient pool and private network were removed after
success. Host-side fixtures were scoped to run `Pi9sqT`; no existing appliances
were reused or modified. The full workload suite invokes this multi-node harness
after its two single-node passes; those regression passes are qualified separately.
Before this final multi-node run, the same ISO passed the focused management-update
appliance gate in 716.51 seconds, including commit, rollback, both crash-recovery
paths and public-status cleanup.

Remaining validation gaps: the appliance suite restarts the isolated management
container, rather than rebooting an already populated isolated VM. A successful
removed-node factory reset, peer-address release and fresh enrollment is covered
by focused reset/release guards but still needs its own appliance acceptance
scenario. Migration and enrollment/recovery checkpoint fault injection have
focused tests; this run used fresh appliances and did not test an upgrade of an
installed older release or power loss at every checkpoint.

### Final single-node regression qualification — 2026-10-31

A worker ran `TestWorkloadConfidence` twice on independent fresh 8 GiB libvirt
appliances using the same final ISO:
`/nix/store/nj8rk2dmlqs8k70x8650ig1whmlfijdn-floatlab-installer-unknown.iso/iso/floatlab-installer-unknown.iso`.
Its control image is
`sha256:741e4c2e546c9032e20112038157b0d9d435cc7b875277b13382acae81c4d21c`.
This build includes the joined-node bootstrap replay guard and SQLite in the
production hostd service PATH. Earlier failed diagnostic images are not qualified.

| Run | Result | Duration | Required API operation coverage |
| --- | --- | --- | --- |
| Full single-node pass 1 | 8/8 scenarios passed | 1431.87 s | 108/108, no missing operations |
| Full single-node pass 2 | 8/8 scenarios passed | 1440.43 s | 108/108, no missing operations |
| Focused management-update gate on the same build | Passed | 716.51 s | Actual preflight failure, commit, rollback and both crash-recovery paths |

Each full pass covered container management; CLI web-service creation, HTTP,
DNS, logging, metrics and restart persistence; documented management contracts;
unconfigured-lab rejection; alert persistence and recurrence; snapshot restoration
and failed-health rollback; network settings; and actual management updates,
including crash-after-install and crash-during-restore recovery. Standalone lab
routes were tested for rejection; successful membership and partition behavior
are qualified by the separate multi-node appliance suite above.

Logs and machine-readable coverage are in
`/tmp/floatlab-single-final-1793423844-1493950/pass-{1,2}.{log,json}`.
The focused gate log is
`/tmp/floatlab-single-final-1793423092-1483728/pass-1.log`.
Both coverage files report `passed: true`, `covered: 108`, `total: 108` and an
empty missing-operation list. All owned domains, disks, pools and networks were
removed; a final libvirt inventory contained no domains.

The final worker Go race suite also passed:
`go test -race -count=1 ./pkg/... ./internal/... ./api/openapi ./integration ./cmd/...`,
with the real pinned rqlite sidecar, native `sqlite3_rsync`, and actual Docker
cached-image/explicit-pull tests enabled. The client/CLI tests, UI tests and
workspace TypeScript checks passed. Client API types were regenerated after the
final schema change. The cached-image test independently verifies zero registry
requests for ordinary cached startup, while explicit upgrades and `always` still
contact the registry and `never` does not.

## Acceptance criteria

- Partition a two-node lab: both nodes create/start/stop/edit their own stacks via
  the normal API/CLI/UI while lab-wide mutations are rejected without quorum.
- Workload operations still succeed if rqlite itself is stopped. Restart a node
  disconnected and reconcile its existing owned workloads from local state.
- Concurrent isolated provisioning has unique IDs and disjoint IP allocation;
  exhausted/unassigned ranges do not cause duplicate address allocation.
- A node authenticates locally using its last applied configuration, preserves
  normal expiry checks, and reports stale authorization. Applied revisions catch
  up when connectivity returns; local root recovery remains independent.
- Empty-node enrollment, expired/reused invitations, bad identities, unauthorized
  operations and populated-lab rejection preserve the existing security guarantees.
- Interrupted join/promotion never creates a duplicate member or partial authority.
- Remove an unavailable voter from a three-node lab with two healthy voters;
  restart the coordinator after each removal checkpoint and prove idempotent
  membership changes, retained tombstones and accurate revocation progress.
- A one-of-two survivor rejects ordinary removal without quorum. Explicit root
  recovery requires fencing, preserves a verified original rqlite backup and
  restores shared administration without interrupting its local workloads.
- Fault-inject every lab-recovery checkpoint; retain diagnostics/backups and never
  automatically bootstrap a conflicting lab or replay peer workload jobs.
- Removal preserves cached databases, project artifacts, backups, images and
  unsynced-history dependencies. No workload takeover, purge or range reuse occurs
  implicitly; explicit destination retirement/resource disposition is required.
- Removed nodes cannot reconnect via TLS, restricted SSH or rqlite membership;
  disconnected members apply revocation before resuming peer work. Fresh enrollment
  requires explicit reset/emptiness checks and a new identity, preserving old backups.
- Commits trigger synchronization without polling; rollbacks do not. Test burst
  debounce, sustained writes, commits during transfer, and the final trailing write.
- Recover a crash between commit and notification using startup/reconnect checks.
  Lost acknowledgements and retry timers cannot duplicate work or block local writes.
- Replication bookkeeping updates do not trigger recursive synchronization loops.
- Run the pinned `sqlite3_rsync` on real concurrent WAL-backed writes; reject wrong
  sources/generations, corruption, stale revisions and incompatible schemas/utility
  protocols. Validate applied revisions independently of notification revisions.
- Interrupted synchronization/publication leaves the last good replica readable.
  Fresh views include deletes/updates without executing source tasks remotely;
  publication does not depend on staging WAL sidecars or raw file-hash equality.
- Verify strict SSH host-key checks, key rotation/revocation and receiver command/
  path confinement. Replication keys cannot open shells, forward connections, write
  local authoritative state, or overwrite another source's replica.
- Every node eventually receives every other node's latest workload database after
  reconnection; local operation never waits for those acknowledgements.
- Cached remote state cannot authorize local execution. Fenced manual takeover and
  returning-node conflicts affect only the implicated workloads.
- Fault-inject migration and store switching; prove no lost state, duplicate
  dispatch or simultaneous old/new workload authorities.
- Run real multi-node acceptance with node/process restarts, partitions, rqlite
  outages, authentication, local provisioning and subsequent replica catch-up.

## Limits

Only lab-wide configuration retains quorum dependence. Offline nodes use potentially
stale security configuration, and asynchronous replicas may lose recent unreplicated
state if an owner is lost. Independent workload operation does not provide safe
automatic takeover, guaranteed zero-data-loss recovery, or an independent off-lab
backup.

The implementation currently uses node-scoped IPv4 service pools for independent
published networking. It rejects the legacy unscoped IPv6 allocation API in peer
mode; node-scoped IPv6 ranges remain an extension. Deleted or narrowed pool
ranges remain reserved, and a workload-range retirement workflow is still needed
before those addresses can be reused. The rotation endpoint covers the local
leaf certificate and database publisher key; lab CA and host SSH-key rotation
require a separate administrative workflow. Unexpected host keys remain rejected.
