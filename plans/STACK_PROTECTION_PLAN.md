# Snapshots, Replication, Clones, and Restoration

Status: agreed design, not implemented. Updated 2026-10-31.

## Goal and dependencies

Protect whole Compose stacks and recover or clone them on either lab node, with
offline-capable images, independent destination retention, and safe local cleanup.

The [multi-node lab plan](MULTI_NODE_LAB_PLAN.md) owns enrollment, authenticated
transport, rqlite lab configuration, owner-local SQLite workload databases,
read-only peer database replicas, and the local recovery infrastructure.
Implement local naming, manifests, capture, and retention before that foundation
is complete if useful; remote transfers and multi-node acceptance depend on it.

Existing local ZFS streaming, snapshot scheduling, image/runtime handling, and
clone/swap restore machinery should be extended. Hostd replication handlers are
currently stubs; a queued response is not evidence of a received recovery point.

Decisions:

- Whole-stack recovery only: Compose/project files, configuration, secrets, all
  managed datasets, and exact container images, including locally built images.
- Live, crash-consistent captures. Application quiesce hooks are deferred.
- Destinations are enrolled FloatLab ZFS nodes in the same lab.
- Local snapshots and remote backups share a capture schedule but retain history
  independently. Source deletion never implicitly deletes remote history.
- New clones start isolated. Production exposure is a separate explicit action.
- Local workload protection continues without lab quorum using owner-local SQLite
  and the last applied lab configuration. Existing authorized transfers may
  continue between reachable enrolled peers; disconnected destinations build backlog.
- Automatic cross-node takeover remains forbidden without quorum. Explicit local
  recovery works through normal local management, with root hostd as a fallback;
  production takeover still requires fencing.

## Workload metadata authority

Persist protection policies, snapshot/recovery metadata, scheduler progress, local
operation locks/checkpoints/idempotency records, source transfer jobs and received
acknowledgements in the source owner's local SQLite database. Destination receive
jobs, backup inventory and retention dependencies belong to the destination's own
SQLite database. Each node publishes its database one way as a read-only view to
every other enrolled node using commit-driven `sqlite3_rsync` over restricted SSH
under the multi-node plan. Database-view replication uses no periodic polling;
startup/reconnection catch-up and retry timers cover missed notifications/outages.

An acknowledgement is sent to the source owner for local persistence, not written
into its peer replica. Database-view replication is independent of workload
ZFS/image replication: receiving a stack record does not mean its data is protected,
and a complete destination backup does not need to wait for global database sync.
Private on-disk manifests remain independently usable for root recovery.
`sqlite3_rsync` only synchronizes workload-state databases; it does not copy ZFS
datasets, project/secret artifacts or image archives. Those transfers keep their
authenticated peer interfaces and completion checks. Coalescing database-view
notifications never permits coalescing unsynced scheduled recovery history.

## RFD-0008 snapshot naming

Follow [RFD-0008](../rfd/rfd-0008.md): a six-character uppercase Crockford Base32
timestamp, three-second ticks, and the `2025-01-01T00:00:00Z` epoch. Truncate to
the interval start and reject out-of-range timestamps. Accept the RFD's lowercase
and transcription aliases on decoding; emit canonical uppercase only.

The RFD defines the timestamp component. This plan defines the full workload names:

| Purpose | Name | Example |
| --- | --- | --- |
| Scheduled protection | `flsnap-<timestamp>-<interval>` | `flsnap-0J8F40-1h` |
| Manual snapshot | `user-<timestamp>-<label>` | `user-0J8F40-before-migration` |
| Pre-restore safeguard | `pre-restore-<timestamp>` | `pre-restore-0J8F40` |
| Pre-upgrade safeguard | `pre-upgrade-<timestamp>` | `pre-upgrade-0J8F40` |

Generate one timestamp component per logical capture, reused across every dataset.
Keep globally unique operation/recovery-point IDs separately. Replication preserves
source snapshot names; do not create a separate `fsrepl-*` snapshot per destination.
Overlapping tiers produce separate operations and names.

Persist the selected name before capture and record operation ownership during
snapshot creation. A retry reuses snapshots only after verifying their ownership
and complete dataset membership. On a foreign collision, retry in the next tick,
up to three ticks, then fail clearly. Never overwrite/delete a conflicting snapshot
or choose different timestamps independently for datasets in one capture.

Continue listing, restoring, and replicating legacy snapshot names without
renaming them. Use manifests and snapshot GUIDs for identity, and ZFS creation
metadata for retention ordering. Mixed legacy/new names must not be sorted to
infer chronology or parsed to infer ownership. Management/OS updater names are
outside this workload naming migration.

Add a small dependency-free Go timestamp codec and shared naming helpers; update
all workload callers and replication bookkeeping together, including existing
sequence/name-based assumptions.

## Policy, scheduling, and recovery contents

Add one stack-level `x-fl-protection` policy using the existing positive
`<interval>/<keep>` grammar and `m`, `h`, `d`, `w`, `mo`, `y` units:

```yaml
x-fl-protection:
  local: "15m/8 1h/24 1d/7"
  destinations:
    - node: backup-node
      snapshots: "15m/8 1h/48 1d/30 1mo/12"
```

Resolve destination names to stable enrolled node IDs. The union of local and
destination intervals determines capture times; each location has its own keep
counts. An omitted local tier retains only transfer/dependency snapshots.
Protection is disabled until explicitly configured.

Preserve existing volume schedules for legacy stacks until explicitly migrated.
Reject simultaneous legacy volume schedules and a new whole-stack policy, and
preview the resulting schedule before migration.

Use UTC. Minutes/hours/days use Unix-epoch interval boundaries; weeks use Monday
UTC; months/years use calendar boundaries anchored to January 1970. The schedule
anchor is independent of RFD-0008's timestamp-encoding epoch. Overlapping tiers
produce separate snapshot sets as specified in the existing execution plan.

Deduplicate work by stack, ownership generation, tier, and scheduled boundary.
After downtime, record missed boundaries and create one current catch-up point
per affected tier; do not invent historical captures. Encode the actual capture
attempt's nominal timestamp, not the missed boundary, and retain both times in
the manifest.

Each immutable recovery manifest records operation/point IDs, compact timestamp,
actual creation time, tier, source node, ownership generation, configuration
revision, exact dataset membership/relative paths/GUIDs/properties, project and
secret files, image identities/platforms/archive hashes, and completion evidence.

Archive exact image content once per image identity per backup host, including
locally built images. Recovery must work without pulling tags or rebuilding.
Validate all persistent dependencies; reject unsupported unmanaged host paths
instead of publishing an incomplete point as successful. Reject execution on an
incompatible architecture.

## Storage, capture, and transfer

| Purpose | Location |
| --- | --- |
| Production stack | `floatlab/<stack-name>` |
| Received datasets | `floatlab/system/protection/<source-node-id>/<stack-id>/<generation>/datasets/<dataset-id>` |
| Manifests | Repository-local `catalog/<point-id>.json` |
| Shared image archives | `/floatlab/system/protection/images/<image-id>.tar` |
| Local recovery journal | `/floatlab/system/protection/journal/` |
| Runnable clone | `floatlab/<new-stack-name>` |

Protection repositories remain outside the management-update rollback tree.
Received datasets stay unmounted/read-only. Manifests reconstruct the original
hierarchy; receive paths never become production mountpoints directly.

Capture and replication transaction:

1. Acquire the stack lock against configuration changes, upgrades, restores, and
   dataset restructuring. Resolve project dependencies and exact image identities.
2. Allocate/persist the RFD-compliant name and atomically snapshot the full tree.
3. Persist the manifest, pin required snapshots, and enqueue destination transfers.
4. Stream missing image archives and individual datasets between authenticated
   host daemons, bounded in memory and authorized for this operation's resources.
5. Increment only from verified common snapshot GUIDs; otherwise seed a new
   lineage. Serialize transfers for each stack/destination.
6. Verify every dataset, image, and manifest before atomically publishing a
   complete recovery point at the destination.
7. Persist destination acknowledgement before reporting success or releasing
   transfer dependencies.

Use resumable receives. Validate resume tokens against their jobs and pinned
source snapshots. Interrupted points stay incomplete and unavailable for restore.
Restart/retry only incomplete work, without duplicate complete points.

Send datasets individually rather than using an authoritative recursive mirror.
Never propagate source deletion or force rollback over retained destination
history. Track dataset additions, removals, and recreation in manifests so older
points remain reconstructible. A failed destination does not block other targets.

## Retention and capacity

Preserve every captured point awaiting destination acknowledgement. Never silently
coalesce pending history. Retain the verified common incremental base for each
destination even when ordinary local retention would expire it.

Remove a surplus local snapshot only when no local-retention requirement, pending
destination, incremental base, resumable receive, clone origin, restore,
pre-restore recovery tree, or administrative pin requires it.

Destination cleanup uses its own tier counts and dependency checks. Source stack
or snapshot deletion does not authorize remote history deletion. Explicit remote
backup deletion is a separate administrator operation. Pin blockers are visible;
cleanup is retryable and never uses broad recursive destruction or global pruning.

Pause new automatic captures below a configurable free-space reserve, defaulting
to the greater of 10% of pool capacity or 5 GiB. Alert on backlog, blocked captures,
and low capacity; continue draining transfers where possible. This guard does not
guarantee that workload writes cannot exhaust the pool.

Garbage-collect image archives only after no retained point, pending job, or clone
references them. Recheck dependencies before deletion, not only when scheduling
cleanup.

## Restore, clone, and manual takeover

### Restore an existing stack

Select an explicit recovery point and target node. Validate completeness, images,
architecture, free space, and networking before stopping anything. Restoring an
already locally owned stack uses its owner's SQLite transaction and normal local
authorization, without an rqlite write. A different node must use an explicit
ownership handoff or fenced takeover; a read-only peer copy grants no authority.

For in-place restoration, stop the stack, preserve pre-restore state, reconstruct
the selected dataset membership in staging, then journal clone/swap activation.
Restore matching project configuration and archived images; restart only if
previously running. Use archived image identities rather than mutable source tags.

Failures preserve the selected backup and pre-restore state, report the reached
checkpoint, and support deterministic retry or rollback.

### Clone on either node

Assign a new stack ID, name, Compose identity, writable datasets, and network
identity. Clone local snapshots with copy-on-write; transfer the chosen point
first if it is not present on the target node.

Start on a dedicated isolated network without published ports or access to host,
production, other nodes, or external services. Reject privileged/device,
host-namespace, and host-socket configurations that defeat isolation. Verify
isolation rather than relying solely on Docker's internal-network flag.

Exposure is a separate administrator action with a fresh IP from the target's
already assigned local address range. It can proceed without lab quorum when
that range has capacity; acquiring/changing shared ranges requires quorum.
Never reuse the source stack's identity for an ordinary clone. Store the clone
in its new owner's local SQLite and publish it as that node's state. Show origin
snapshots as pinned until their clone dependencies are removed.

### Local recovery without quorum

Use owner-local SQLite and cached lab configuration through normal local API,
CLI and UI; root hostd plus private manifests/journals remain the recovery fallback.
Local create/start/stop, snapshot schedules, restore, clone and safe cleanup are
normal operations during quorum loss, not emergency writes to shared rqlite.
Shared lab changes and automatic cross-node ownership takeover remain blocked.

Production takeover requires explicit fencing confirmation, the selected point,
and acknowledgement of possible data loss. A timeout or failed ping is not proof
of fencing. Reuse the production IP only if the original instance is fenced and
the target network supports it; otherwise require an explicit target address.
Cross-site IP mobility is deferred.

Journal a new ownership generation and mark the takeover unresolved against lab
ownership. Keep subsequent production lifecycle actions explicit while takeover
is unresolved; local safety snapshots remain allowed, but do not automatically
replicate the new lineage into the former owner's repository. On reconnection,
quarantine conflicting instances and require an administrator to select the
authoritative instance before resuming automatic lifecycle/lineage reconciliation.
Unrelated locally owned stacks and their protection continue normally. Preserve
both histories; never merge divergent application data or fail back automatically.

## Interfaces and implementation sequence

Extend management API/client/CLI/UI for policy editing, node-scoped recovery-point
browsing, transfer progress/retry/cancel, restore, clone/exposure, and explicit
backup deletion. Reuse administrator authentication, idempotency keys, and
asynchronous operation IDs. Mutations accept resource IDs, not arbitrary paths.

Hostd and peer streaming interfaces return durable completion evidence, not just
queue acceptance. Root CLI recovery works independently of management availability.
Show lab quorum separately from local workload availability, recovery-point age,
destination completion, source database-view revision/age, pending history,
space pressure, and cleanup blockers. Route workload mutations to the actual
owner and never execute them against read-only peer views.

Implementation order:

1. RFD codec/naming helpers, legacy compatibility, manifests, and local capture.
2. Image archives, complete/resumable replication, and acknowledgement using the
   multi-node plan's authenticated transport and authority checks.
3. Unified policies, scheduling, dependency-aware retention, and capacity alerts.
4. Restore, isolated clones, local takeover, and ownership reconciliation.
5. Shared API/client/CLI/UI integration, migration guidance, and appliance tests.

## Acceptance criteria

- RFD vectors, tick boundaries/ranges, aliases, uppercase output, shared names,
  foreign collisions, partial capture/restart ownership, and legacy compatibility.
- UTC interval/calendar boundaries, arbitrary interval lengths, overlapping tiers,
  missed-run reporting, catch-up, and duplicate suppression after local restarts.
- Full/incremental/resumed transfers, tree changes, bad GUIDs, corrupt archives,
  disk-full, cancellation, and process/power interruption at each checkpoint.
- No complete point is published before every dataset, image, and manifest is
  verified and durable. Queue acceptance cannot masquerade as successful backup.
- Pending history survives outages; destination expiration is independent; base,
  clone, restore, and administrative dependencies prevent unsafe cleanup.
- Source deletion leaves remote points usable. Image garbage collection preserves
  every dependency and never removes unrelated Docker resources.
- With registry access blocked, restore exact images/project/data and run clones
  on either compatible node. Reject incomplete or incompatible recovery points.
- Verify clone isolation, fresh-IP exposure, pre-restore preservation, and safe
  retry/rollback after interrupted activation.
- With rqlite quorum lost or rqlite stopped, create/start/stop local stacks,
  capture/retain snapshots, restore and run new isolated clones through normal
  local interfaces. Prove those commits never wait on peer database replication.
- Continue authorized backup transfers between reachable peers without lab quorum;
  unavailable targets retain pending history and catch up after reconnection.
- Distinguish stale database views from verified complete workload backups. Received
  peer records never execute their tasks or confer ownership on the receiver.
- Confirm commit-driven database-view replication can catch up independently of
  ZFS/image transfer scheduling. Retrying a backup or its acknowledgement does not
  create a database-replication acknowledgement feedback loop.
- Exercise fenced takeover and returning-owner conflicts; automatic execution is
  blocked for affected instances without stopping unrelated local protection.
- Run real multi-node ZFS/Docker appliance acceptance, including prolonged target
  outages, quorum loss, node reboot, and restored application data verification.

## Limits

Recovery is crash-consistent and depends on complete protection material and
administrator-provided fencing. Enrolled-node replicas are not a substitute for
an independent off-lab backup; object storage and independent-lab targets remain
deferred.
