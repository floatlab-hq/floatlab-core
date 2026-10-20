# Management alert rule catalogue

Status: proposed rule catalogue, 2026-10-20. See the
[implemented evaluator](../../pkg/alerts/README.md) for current capabilities and
configuration. This document defines the rules and
evaluation behaviours needed by an evaluator running in the management service.
It does not implement rules or promise that their inputs are already collected.
Thresholds below are initial product defaults for review, not hardware guarantees.

## Scope and existing integration points

The evaluator should cover hosts, managed workloads, storage, data protection,
and the management services themselves. An alert describes an actionable ongoing
condition; a notification reports its transition. Routine successful operations
belong in the event log.

Existing code to reuse or reconcile during implementation:

- [Rule and alert API](routes_config.go): `alert_rules` stores a condition string,
  severity, scope, duration, annotations and enabled state; `/alerts` reads incidents.
- [Compose rules](../../pkg/compose/lifecycle.go): `x-fl-alert-rules` supports numeric
  comparisons for container CPU, memory, restarts and managed volume space.
  [Orchestrator persistence](../orchestrator/orchestrator.go) writes these into
  `stack_alert_rules`.
- [Lifecycle status](routes_lifecycle.go): `alert_status` and the transition API
  represent pending/firing/resolved state per stack rule, with lifecycle events.
- [External webhook](../../pkg/stats/alerts.go): persists firing/resolved incidents
  and creates notifications on first firing. Its identity is currently only
  `(rule_id, stack_id, node_id)`, so two disks, pools or services can collide.
- [Notifications](../../pkg/notify/README.md): existing persistence and SSE delivery.
- [Host metrics](../agent/metrics.go): dataset used/available/quota/snapshot space,
  pool I/O and block-device I/O. [Docker telemetry](../../pkg/docker/telemetry.go),
  [events](../../pkg/docker/events.go) and [health](../../pkg/docker/health.go)
  provide some workload inputs. Additional collection is required below.

`pkg/stats/README.md` describes default rules and metrics that must not be assumed
to be implemented. The root Compose file also configures vmalert. Assign each rule
to one evaluator during migration so both paths do not send duplicate incidents.
Existing custom condition strings and Compose rule names need a compatibility
decision before changing their representation. This catalogue does not select a
new expression language or database schema.

## Deferred: setup alerts

Setup alerts will identify incomplete or unsuitable configuration. Initial
candidates include single-node setup as well as multi-node prerequisites:

| Candidate | Setup condition to surface |
| --- | --- |
| Two-factor authentication (2FA) | 2FA has not been configured. |
| Domain name | A domain name has not been set. |
| IP forwarding | Required IP forwarding has not been enabled. |
| Notification endpoint | No notification endpoint has been configured. |

Eligibility, scope (user/node/deployment), severity, dismissal policy and lifecycle
will be defined later, alongside the remaining multi-node setup requirements.
These candidates are outside the initial evaluator scope; do not implement checks
or add configuration for them yet. Keep them distinct from runtime failures of
already configured resources, such as a notification endpoint failing delivery.

## Evaluation primitives

These are reusable behaviours, not a separate engine for each alert name.

| Primitive | Required behaviour | Examples |
| --- | --- | --- |
| Threshold | Compare a gauge or derived ratio; support upper and lower bounds | RAM, disk space, temperature |
| Sustained condition | Predicate stays true for a duration; distinct recovery threshold and duration | Load, RAM, unhealthy service |
| State match | Match boolean, enum, or decoded bit flags | SMART failure, pool degraded |
| Window count / increase | Count unique events or counter increases over a moving window | Restart loop, OOM, I/O errors |
| Freshness / absence | Compare last valid observation with a deadline and expected inventory | Missing host, missing metrics |
| Schedule deadline | Detect missed starts/completions from configured schedule plus grace | Replication, snapshots, backups, scrub |
| Progress timeout | Running work exceeds runtime or makes no progress for a duration | Stalled replication or resilver |
| Desired versus observed | Compare expected instances/state against current observations with startup grace | Service never starts, lost replica |
| Compound condition | Combine scoped predicates with AND/OR and prerequisite checks | Low free bytes OR high used percentage |

Do not require forecasting, anomaly detection, arbitrary scripts or log-pattern
matching for the initial evaluator. The catalogue below can be expressed using
these primitives and explicit input adapters.

## Rule catalogue

`W` = warning, `C` = critical. `for` means continuously true across fresh
observations; a window count is not a sustained-duration check. Immediate means
the next valid evaluation. Defaults apply only to eligible, enabled resources.

### Host resources

| Rule ID | Scope and input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `host.load.high` | Node; 5-minute load average / online logical CPUs | W > 1 for 10m; C > 2 for 10m | < 0.8 for 5m. Load includes runnable and uninterruptible tasks; include CPU and I/O evidence to explain it. |
| `host.cpu.high` | Node; non-idle CPU rate over 5m | W > 90% for 10m; C > 98% for 15m | < 80% for 5m. Related to load, but a distinct signal. |
| `host.memory.high` | Node; `1 - MemAvailable / MemTotal` | W > 90% for 5m; C > 95% for 5m | < 85% for 5m. Do not use free RAM alone or add ARC usage again. |
| `host.swap.pressure` | Node; swap-in/out rate and available memory | W sustained swap activity for 10m AND memory utilization > 90% | Both conditions clear for 5m; no swap configured is not a fault. Activity threshold must be configured in bytes/s. |
| `host.oom` | Node; unique kernel OOM kill events | C on any kill in 5m | No new kill for 15m; retain affected process/service evidence. |
| `host.temperature.high` | Node/sensor; temperature and device limits | W above configured warning limit for 5m; C above critical limit for 1m | Below warning limit minus configured hysteresis for 5m; no universal temperature default. |

### Filesystems and ZFS capacity

| Rule ID | Scope and input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `filesystem.space.low` | Node/mount; total and bytes available to writers | W > 85% used OR < 10 GiB available for 10m; C > 95% OR < 1 GiB for 2m | Below warning used threshold minus 5 percentage points AND above warning free floor plus 1 GiB for 5m. Set smaller floors for small filesystems. |
| `filesystem.inodes.low` | Node/mount; available/total inodes | W < 10% for 10m; C < 3% for 2m | > 15% for 5m; only filesystems with meaningful inode limits. |
| `filesystem.readonly` | Node/mount; expected writable and observed read-only | C for 1m | Writable for 2m; exclude deliberately read-only mounts. |
| `zfs.pool.capacity.high` | Node/pool GUID; allocated/size and free bytes | W > 80% for 10m; C > 90% for 5m; C immediately if no usable space | < 75% for 5m; capacity is separate from pool health. |
| `zfs.dataset.space.low` | Node/dataset; effective available space and enforced quota/refquota if present | W > 85% of applicable quota OR < configured free floor for 10m; C > 95% OR no writable space for 2m | Below warning percentage minus 5 points AND above free floor plus configured margin for 5m. Dataset available can be constrained by parents or pool space. |

Monitor boot/root, management data, logs/metrics data and workload storage.
Exclude pseudo filesystems, container overlay views and duplicate bind mounts;
deduplicate filesystem and dataset observations of the same storage. Unlimited
quotas and absent/zero denominators produce no ratio, not 100% usage. Snapshot
space is useful incident evidence, not a second copy of allocated space.

### Replication and protection jobs

Scope replication by `(stack, dataset, source node, destination node)`. Each
destination has an independent recovery point. A queued task, source snapshot,
or successful sender alone does not establish a usable destination copy.

| Rule ID | Input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `replication.schedule.missed` | Expected run and actual start/completion | W when due run has not started by due + grace; C when it has not completed by its completion deadline | Matching run completes successfully; distinguish scheduler delay from transfer failure. |
| `replication.recovery_point.stale` | Timestamp of source snapshot most recently confirmed received at destination | W age > configured warning age; C age > recovery point objective (RPO) | Confirmed received point is within warning age. A newly completed transfer of an old snapshot can remain stale. |
| `replication.failed` | Unique transfer outcomes and failure class | W on failed attempt; C after 3 consecutive failed attempts or an unrecoverable failure | Successful verified transfer; a running retry does not resolve it. |
| `replication.stalled` | Running transfer, progress timestamp, runtime | W no progress for 10m; C no progress for 30m OR configured maximum runtime exceeded | Progress resumes for 2 evaluations or transfer succeeds; a failed transfer changes to failure condition. |
| `replication.unavailable` | Expected target and prerequisites | W target inaccessible for 5m; C no usable failover copy when one is required | Target accessible and required copy verified; retain stale-copy state independently. |
| `snapshot.schedule.missed` | Per dataset/tier schedule, successful snapshot timestamp | W completion deadline missed; C configured maximum snapshot age exceeded | New valid snapshot satisfies that tier; an enqueue timestamp is insufficient. |
| `backup.failed_or_stale` | Per backup destination; outcome and last verified recovery point | W failed run or missed completion deadline; C maximum backup age exceeded | Verified successful backup within age limit; replication is not evidence of backup success. |
| `backup.verification.failed` | Verification/restore-test result and schedule | C failed verification; W verification deadline missed | New successful verification of a usable backup. Enable only where verification is configured. |

A protection policy must supply schedule/timezone, start grace, completion budget,
warning age and maximum age/RPO. Use actual scheduled occurrences, including
calendar intervals and daylight-saving changes; never assume every job runs daily.
For interval policies a suggested start grace is `max(2m, 10% of interval)`;
completion budget must reflect expected transfer size. Warn-age must be below RPO.
Never-successful jobs are pending until the initial completion deadline, then
alert explicitly as “no successful copy”. Initial seeding needs its own bounded
completion budget; it must not silently mark protection healthy. Disabled policies
and intentional pauses must be visible as unprotected/paused, not missed runs.

### Services and containers

Use stable `(stack, service, replica)` identity across container replacement.
Observe the currently assigned active node, including failover to a backup node.

| Rule ID | Input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `service.start.failed` | Desired running state, operation outcome, start timestamp | C terminal start failure, or still not running/ready after configured health timeout | Ready for 2m. Reuse `x-fl-health-timeout` (currently defaults to 2m); do not add another full timeout after it expires. |
| `service.restart_loop` | Deduplicated starts/restarts/unexpected exits across replacements | W >= 3 restarts in 5m; C >= 5 in 10m | Running/ready with no restart for 10m; do not sum multiple observations of the same event. |
| `service.unhealthy` | Docker health status | W unhealthy for 2m; C for 5m | Healthy for 2m. Respect start period; absent health check means unavailable readiness evidence. |
| `service.instances.missing` | Desired instance count versus running/ready count | W shortage for 2m; C zero available for 1m after startup grace | Desired count ready for 2m; intentionally stopped/scaled-down services are excluded. |
| `service.oom` | Container OOM events | W on any kill; C >= 3 in 10m | No kill for 15m and service ready. Correlate with node OOM. |
| `service.memory.high` | Working set / enforced memory limit | W > 90% for 5m; C > 97% for 5m | < 85% for 5m; no limit means no limit-based ratio. |
| `service.cpu.high` | CPU usage / configured CPU entitlement | W > 90% for 10m | < 80% for 5m; opt-in, avoid treating legitimate batch work as broken. |

Successful one-shot jobs, disabled Compose profiles, manual stops and planned
upgrades must not appear as failed long-running services. A service can be running
but unhealthy. Poll state as well as consume events so dropped events do not hide
failures; query/inspect errors are unknown observations, not healthy state.

### Physical disks and SMART

Scope by node and stable disk WWN/serial, with current path as display metadata.
Collect structured SMART data on the host with bounded timeouts. Support ATA,
SAS/SCSI and NVMe separately; do not interpret vendor-specific raw values using
one universal attribute map. A SMART “passed” result does not prove disk health.

| Rule ID | Input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `disk.health.failed` | Overall health and decoded critical flags | C immediately on failed health, NVMe reliability/read-only warning or equivalent device failure | Explicit healthy report; preserve the failure in history. Decode other NVMe flags into their specific conditions. |
| `disk.media.errors` | Pending/uncorrectable sectors, media errors, grown defects | W existing actionable defects; C new uncorrectable errors | No further increase for configured quiet period for delta alerts; unresolved pending defects remain active. Preserve baseline defects in inventory. |
| `disk.errors.increasing` | Reallocated sectors or interface/CRC error counters | W positive increase in 1h | No new errors for 24h; distinguish media from cable/controller evidence. Counter reset/replacement is not negative growth. |
| `disk.wear.high` | Endurance used and available spare versus device threshold | W endurance used >= 90%; C >= 100% or spare below device threshold | Fresh report below warning threshold or replacement; endurance values need protocol-specific interpretation. |
| `disk.temperature.high` | Current temperature and supported device limits | W above warning limit for 5m; C above critical limit for 1m | Below warning minus hysteresis for 5m. Requires reported or configured limits. |
| `disk.self_test.failed` | Latest completed self-test result/time | W failed test; C uncorrectable read failure | Successful newer test or replacement; aborted tests are distinct from media failures. |
| `disk.self_test.overdue` | Configured test schedule and successful test timestamp | W deadline missed | Successful matching test; opt-in where tests are scheduled. |
| `disk.health.unavailable` | Expected SMART capability and last successful read | W collection failing/stale for 15m | Successful read. Mark genuinely unsupported/blocked-by-bridge devices as unsupported, not healthy. |

Do not equate every nonzero `smartctl` exit code with disk failure: its status is
a bitmask including collection errors and health findings. Avoid waking sleeping
disks solely to meet a default polling interval; freshness must follow that disk's
collection policy.

### ZFS health and integrity

Scope by node/pool GUID, adding vdev GUID where appropriate. Preserve structured
state and reasons rather than comparing an undocumented numeric health code.

| Rule ID | Input | Proposed trigger | Recovery / qualification |
| --- | --- | --- | --- |
| `zfs.pool.degraded` | Pool/vdev state and redundancy | W DEGRADED for 1m; C lost required redundancy or faulted data vdev | Expected topology healthy for 5m. Report affected devices and remaining redundancy. |
| `zfs.pool.unavailable` | Required pool inventory, import state, health, suspension | C unavailable, faulted, suspended or unexpectedly missing for 1m | Imported, accessible and healthy for 2m; deliberate export/maintenance is qualified separately. |
| `zfs.device.fault` | Leaf vdev state and role | W unexpected OFFLINE/REMOVED for 1m; C FAULTED/UNAVAIL on required data/log/special device | Device healthy or expected replacement complete; cache/spare issues carry role-specific severity. |
| `zfs.io.errors` | Per-vdev read/write/checksum counter increases | W any increase in 10m; C repeated errors while redundancy is already lost | No new errors for 1h and pool healthy; retain cumulative counts as evidence. |
| `zfs.data.corruption` | Reported permanent data errors | C immediately | Explicit fresh evidence that reported corruption is repaired/removed and integrity verified; ONLINE alone does not resolve it. |
| `zfs.scrub.failed_or_overdue` | Scrub result and configured completion deadline | W missed deadline or failed/aborted scrub; C uncorrectable data errors | Successful newer scrub within policy; repaired errors remain incident evidence. |
| `zfs.resilver.stalled` | Resilver state, progress timestamp and runtime budget | W no progress for 30m; C maximum recovery runtime exceeded | Progress resumes for 2 evaluations or resilver finishes; normal resilvering is informational. |

Scrubs/resilvers are not failures merely because they are running. Do not require
a pool format upgrade as a health fix. Account for intentional offline devices,
replacement identity and different vdev roles when deriving severity.

### Availability, management and adjacent rules

These use the same primitives and should shape the initial design even where
their collectors or features arrive later.

| Rule ID | Scope/input | Proposed trigger and recovery |
| --- | --- | --- |
| `node.unreachable` | Expected online node; management/host heartbeat | W absent for 1m; C 5m; recover after 2m of valid heartbeats. |
| `telemetry.stale` | Node/collector/resource; expected collection cadence | W no fresh observation for max(3 intervals, 2m); C 10m for required fast health collectors; recover after 2 valid observations. Override deadlines for slow SMART/test collectors. |
| `management.dependency.unavailable` | rqlite, VictoriaMetrics, hostd, Docker and required network/DNS dependencies | W failed probes for 1m; C 5m; recover after 2m successful. Probe only configured dependencies. |
| `management.quorum.at_risk` | Management Raft and configuration-store membership/leader health, separately | W loss of redundancy for 1m; C no leader/quorum for 1m; recover after stable quorum for 2m. Requires an observer that can still run. |
| `management.task.stalled` | Pending/running task age, lock/progress and type-specific budget | W budget exceeded; C 2x budget; recover on progress/completion or transition to failed operation. |
| `management.operation.failed` | Terminal lifecycle/failover/restore outcome | C failed operation; retain incident until acknowledged or superseded by verified successful recovery. Preserve step/error evidence. |
| `network.pool.exhausted` | Allocatable addresses after reservations | W < 10% free for 5m; C zero; recover > 15% for 5m. |
| `certificate.expiring` | Managed certificate expiry | W < 30 days; C < 7 days or expired; recover after valid renewal with > 30 days remaining. |
| `clock.offset.high` | Node clock synchronization/offset | W absolute offset > 5s for 5m; C > 30s for 5m; recover < 1s for 5m. |
| `notifications.delivery.failed` | Configured delivery sink; attempts and pending age | W 3 failures in 10m; C oldest undelivered critical notification > 10m; recover after delivery succeeds and backlog drains. |

The evaluator cannot reliably report its own total outage, a dead appliance or a
database outage through that same failed path. Keep an externally observable
health/last-evaluation signal; external outage delivery is a separate deployment
requirement, not something an in-process rule can guarantee.

## Shared lifecycle and noise control

1. Evaluate every 30s initially; freshness is based on source timestamps, not the
   time an old query result was fetched. Track valid/unknown/unsupported separately
   from healthy. Reject invalid values, future timestamps outside permitted skew,
   NaN, invalid denominators and malformed enum/protocol data.
2. Per instance use inactive -> pending -> firing -> resolved. Unknown data does
   not resolve a firing alert or advance a pending/recovery duration. Break pending
   continuity on a stale gap; on fresh data start its duration again. Expose unknown
   status and raise the relevant collection/dependency alert.
3. Separate lookback windows, firing duration, startup grace, maximum sample age,
   and recovery duration. Evaluate warning/critical bands independently; critical
   must not wait for warning to fire. Default recovery for rows without an explicit
   duration is two valid evaluations satisfying their stated recovery condition.
4. One incident per rule and stable resource identity, with severity updated on
   escalation/downgrade. Fingerprint includes service/replica, mount, disk, pool,
   vdev or replication destination as needed, not volatile container IDs, metric
   values or severity. Track first observed, pending/firing times, last evaluated,
   current evidence, recovery time and recurrence separately.
5. Notify on first firing, severity escalation and recovery. Do not notify every
   tick. Start with no periodic reminders; notification read/acknowledged/silenced
   state must not change the measured condition. Use an idempotent durable handoff
   so persistence succeeds before delivery and retries do not duplicate events.
6. Silence/maintenance suppresses delivery while recording evaluations. Planned
   stop/start/failover grace is bounded; it must expire even if work hangs. Never
   suppress unrelated corruption/SMART failures merely because an upgrade runs.
   On silence expiry notify once if the condition is still firing.
7. Node unavailable inhibits child missing-service/collector notifications;
   unavailable pool inhibits consequent dataset-space/replication delivery.
   Keep child evidence and independent data-loss/freshness conditions visible.
8. Removing/disabling a rule or resource closes its incidents with that reason,
   not “healthy”. Material condition edits reset pending evaluation. Adding a new
   device/job starts its own baseline and bounded initial grace.
9. Use one active evaluator owner (existing coordination where suitable), bounded
   query/collector timeouts and batches. Persist transition/deduplication state
   across restart/leader change. Continue a duration only with retained fresh
   evidence proving continuity; downtime itself does not count as healthy/breached.
   Counter windows must tolerate resets, duplicate/out-of-order events and reboot.

Every incident should include the resource and scope, observed value/state with
units, threshold/deadline, duration, source observation time, relevant job or
operation ID, likely consequence, and a concrete next action. Redact credentials
and sensitive command output from error evidence. Validate and authorize custom
rules; evaluation must not become arbitrary shell execution.

## Input gaps and implementation reach

| Input group | Current foothold | Required additions / verification |
| --- | --- | --- |
| Host load/RAM/CPU/filesystems | Node stats API queries node-exporter-style series | Verify exporter deployment and stable node labels; collect online CPUs, load, available memory, mounts/inodes, read-only state and heartbeat freshness. API queries alone do not establish collection. |
| Dataset/pool capacity | Agent exports `fl_zfs_dataset_*` | Effective quota/refquota/ancestor constraints, pool size/allocation/free, pool GUID and independent collection status. Current dataset inventory cannot discover every missing/unimported pool. |
| Disk/ZFS health | ZFS execution layer and pool I/O counters | Physical disk inventory, SMART capability/results, pool/vdev state/reasons/errors, scrub/resilver history/progress. I/O counters do not establish health. |
| Workload health | Docker state/health, events, usage telemetry | Desired replicas/state, stable identity, replacement-aware restart/OOM windows, limits/entitlements, event continuity and observation errors. |
| Replication/protection | Task queue, replication worker, schedules, snapshot records | Durable per-destination successful receive/recovery-point identity, source snapshot time, expected deadlines, attempt outcome, progress/runtime and initial-seed policy. Task success alone must be checked against destination evidence. |
| Management/adjacent | Health routes, membership, task/operation state | Dependency probes, evaluator heartbeat, quorum observer, budgets, clock/certificate signals and configured delivery-sink outcomes. |

Implementation will need to reach management startup/shutdown and leadership,
input collectors/host IPC, both existing rule stores, incident and notification
persistence, lifecycle events, API/OpenAPI/client contracts, Compose validation,
configuration/defaults and deployment wiring. Preserve existing custom rules and
incident history through explicit migrations. UI should distinguish firing,
unknown, unsupported, disabled and silenced conditions when those are exposed.

Implement the seven requested areas plus heartbeat/data-quality rules first;
add adjacent rules as their features and inputs exist. Each enabled rule must
have its required collector or report unsupported/unknown explicitly. Do not
silently ship an enabled rule whose data never arrives.

## Acceptance scenarios for the eventual evaluator

- Short load spike stays inactive; sustained normalized load fires once; recovery
  hysteresis prevents flapping. RAM cache alone does not look like exhaustion.
- Full root mount, full pool and quota-constrained dataset are detected; unlimited
  quotas, inode-less filesystems and duplicate mounts do not generate false alerts.
- Two unhealthy disks, services or replication targets produce separate incidents;
  container replacement does not reset a service restart window.
- Replication never started, failed, stalled, completed late, and transferred an
  old snapshot are distinguishable. One healthy destination cannot hide another
  stale copy; retries do not advance last-successful recovery point prematurely.
- Missing initial replication/snapshot/backup history fires after its deadline;
  disabled policies and maintenance do not appear as successful protection.
- Slow startup respects its configured grace; failed start, restart loop, OOM and
  unhealthy-but-running are detected; successful one-shot/manual stop is excluded.
- SMART passed-with-errors, unsupported bridge, bitmask collection failure and
  NVMe critical flags are handled separately; disk replacement resets baselines.
- DEGRADED, missing pool, faulty cache versus data vdev, permanent corruption,
  counter reset, overdue scrub and normally progressing resilver behave distinctly.
- Missing/stale metrics do not resolve incidents; node/dependency loss groups
  downstream notifications while independent corruption remains visible.
- Repeated evaluation/webhook replay, restart, owner change and notification retry
  do not duplicate incidents or lose firing/recovery transitions. An outage gap
  does not satisfy a sustained threshold or recovery period.
- Warning escalates directly to critical, recovers or downgrades correctly;
  silence expiry, rule edits/removal and resource deletion preserve clear history.
- Calendar/timezone boundaries and clock skew do not create duplicate deadlines
  or falsely fresh recovery points. Invalid custom rules fail validation before use.

## Technical references

- [Linux `/proc` documentation](https://www.kernel.org/doc/html/v6.11/filesystems/proc.html):
  load and memory inputs; `MemAvailable` estimates memory available without swapping.
- [OpenZFS pool status](https://openzfs.github.io/openzfs-docs/man/v2.4/8/zpool-status.8.html):
  health, vdev GUIDs, data errors and scrub/resilver progress. Match collection
  capabilities to the appliance's installed ZFS version.
- [smartmontools `smartctl` manual source](https://github.com/smartmontools/smartmontools/blob/master/smartmontools/smartctl.8.in):
  protocol-specific health data and exit-status bitmask semantics.

The numerical defaults and lifecycle policy above are FloatLab proposals, not
recommendations asserted by these references. Calibrate resource thresholds and
protection deadlines against representative appliances before enabling defaults.
