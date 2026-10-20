# Management Stack Self-Update Plan

Status: implementation added; real appliance acceptance remains unverified. Updated 2026-10-20.

## Summary and decisions

The local hostd coordinates preflight testing, management upgrades, and rollback. Management requests an update and displays progress; hostd completes recovery independently of the management containers and rqlite.

The update unit includes the control-plane backend/UI, rqlite, VictoriaMetrics, VictoriaLogs, and their Compose configuration. Workload containers keep running during management maintenance. This first implementation supports a standalone appliance with a single-node rqlite deployment; coordinated cluster upgrades are deferred.

The deployment uses a disposable cloned stack for preflight, followed by an in-place production upgrade. The preflight clone is never promoted. Production receives a fresh rollback snapshot after its services have stopped. Failed production upgrades stop the stack, restore that snapshot and the old service definitions, and restart the old stack.

Host OS staging, trial boots, and boot promotion are specified in [HOST_OS_UPDATE_PLAN.md](HOST_OS_UPDATE_PLAN.md).

## Ownership and persistent layout

- Add the update coordinator within hostd, reusing existing Docker, ZFS, IPC, release validation, and process-execution facilities. Run one local update transaction at a time, including OS activation and development-mode changes.
- Persist the transaction journal under `/floatlab/system/updates`, outside every dataset restored by management rollback. Record the operation ID, actor, phase, source and target releases, exact image digests, saved Compose configuration, snapshot/dataset identities, test results, errors, and rollback outcome. Flush checkpoints before destructive transitions and resume them on hostd restart.
- Put management state beneath `floatlab/system/management`, with child datasets for rqlite, application Raft, VictoriaMetrics, and VictoriaLogs. Include management-owned persistent configuration needed to restart those services. Keep Compose and image metadata in the transaction record as well as their installed locations.
- Keep appliance identity, credential keys, networking configuration, Docker's shared data root, updater records, and workload datasets outside this tree. Never snapshot or roll back all of `floatlab/system` for a management update.
- Adopt existing management child datasets in a one-time maintenance operation: stop their writers, record existing names and mountpoints, move them under the management parent, set their intended mountpoints, and update Compose binds. Preserve data and make interrupted adoption resumable before enabling updates. Fresh installations create the intended hierarchy directly.
- Store disposable test clones outside the production management tree so recursive snapshots cannot include earlier test stacks. Enumerate every filesystem in a recursive snapshot and clone each separately; ZFS has no recursive clone command.

## Update transaction

1. **Prepare.** Resolve an explicitly selected official release over HTTPS. Validate manifests, architecture, host compatibility, supported rqlite source versions, and exact artifact hashes/image digests. Download all images before isolation. Check pool health, available space, and capacity to run the test stack. Save the old definitions and ensure its images remain locally available. Reject concurrent update, rollback, or mode-switch operations.
2. **Snapshot the running stack.** Take a recursive preflight snapshot and clone its dataset tree to operation-specific mountpoints. Production continues running. This is a crash-consistent test copy, distinct from the later clean rollback snapshot; recovery from it is part of the test.
3. **Test the candidate.** Start the target management image and target sidecar versions against the clones in the sandbox below. Run bounded self-tests and collect a structured result. If testing fails, remove only this operation's test resources and report the failure; leave production running.
4. **Quiesce production.** Block new management mutations, pause scheduling, failover, and task dispatch, and drain existing work. Abort before changing production if operations cannot reach a safe stopping point. Persist the restart-policy suppression described below before stopping any management container. Stop all management containers cleanly, including stateful sidecars. Disable competing systemd/Compose startup paths during the transaction and verify no writer remains. Hostd may continue running workloads, but pauses telemetry writes to the stopped management sidecars.
5. **Take the rollback snapshot.** Recursively snapshot the stopped production management tree and hold the snapshots. Persist their complete membership and the previous service definitions before installing anything new. Never substitute the earlier preflight snapshot: production may have changed while testing ran.
6. **Upgrade in place.** Install the target pinned Compose configuration and start the target services on the existing production datasets. Start rqlite and observability dependencies before management. Gate workload-mutating controllers until final readiness checks pass. Verify actual running versions, migrations, database reads/writes, application Raft recovery, local hostd connectivity, and sidecar health. Allow five minutes for preflight and five minutes for production readiness; require 60 seconds of stable readiness before automatic management commitment.
7. **Commit.** Durably record success, resume controllers and normal management access, and retain the previous release, images, and rollback snapshot. A management-only update commits automatically. When part of an OS trial, defer final commitment and retention cleanup to explicit OS commitment.
8. **Rollback on failure.** Stop the entire upgraded management stack and verify its writers have stopped. Restore every production management dataset to the final rollback snapshot, restore the exact previous Compose configuration and image pins, and restart and verify the previous stack. Record that the upgrade failed even if rollback succeeded. If restoration or old-stack startup fails, stop retrying the candidate and expose recovery status through hostd.

Rollback is an offline operation: all management datasets must reach the same checkpoint before any service restarts. Inspect newer snapshots and clone dependencies before restoration. Remove only newer resources owned by this transaction; unexpected snapshots or dependent clones cause a reported recovery failure rather than recursive destruction of unrelated state.

On restart, hostd reconciles the journal with actual containers, dataset GUIDs, snapshots, and installed definitions. An interrupted production upgrade resumes recovery before normal core-stack startup. Recovery must remain available while rqlite and management are offline. A standalone rqlite data directory retains its node identity and service aliases through test and production restarts; the sandbox's separate network prevents identity collisions.

### Container startup and restart ownership

Docker starts containers independently of systemd's core-stack ordering. Before production changes, journal each management container's original restart policy, set and verify its Docker policy to `no`, and persist a Compose override with `restart: "no"` for every management service. Every transaction startup, including recreated candidate and rollback containers, must use that override. Preflight containers also use `restart: "no"`. Do not change workload-container restart policies or disable Docker globally.

The suppression checkpoint must be durable and all existing policies verified before the rollback snapshot or candidate startup is permitted. If hostd dies while applying suppression, it reconciles that phase before advancing; production data has not changed yet. After Docker or appliance restart, management containers remain stopped until hostd reconciles the transaction, completes any required restoration, and explicitly starts the authorized service versions. Gate both `floatlab-core-stack.service` and hostd's core-service restore path on this reconciliation; startup ordering alone is insufficient.

Restore the recorded normal policies only after management commitment, or after the old stack has been fully restored, started, and verified following rollback. Persist authorization before restoring policies so an interrupted policy change is resumable. For an uncommitted OS trial, retain suppression through the entire trial and let hostd supervise authorized container restarts; release it only after explicit OS commitment or completed fallback recovery. If reconciliation or recovery fails, leave suppression active and management stopped.

## Sandbox and internal self-tests

- Start all test services on a dedicated internal Docker network. Allow traffic among those services, but enforce denial of traffic to production networks, host TCP/UDP services, external networks, and external DNS, covering both IPv4 and IPv6. Verify these properties rather than relying solely on Docker's `internal` flag.
- Publish no test ports. Hostd invokes the internal self-test through the container/network namespace and reads its result. Preload images so test startup requires no registry access.
- Give test containers only their cloned writable state, necessary read-only configuration, and a separate restricted IPC socket such as `/run/floatlab/preflight/hostd.sock`. Mount its dedicated directory rather than the production `/run/floatlab` directory. Exclude the production socket, Docker socket, writable host paths, host networking, privileged mode, and host namespace access.
- Add a preflight startup mode that runs migrations and local startup checks while disabling real workload reconciliation, queued task execution, scheduling, failover, remote-node connections, and external notifications. Reads may use the restricted local hostd connection. Writes required by database self-tests stay in the cloned databases.
- Hostd serves the restricted socket using a separate default-deny handler registry. Reuse audited read handlers for `sys.info`, `sys.version`, `docker.list`, `fs.pool.list`, `fs.pool.health`, `fs.dataset.list`, and `fs.snapshot.list`. Reject all other commands, including exec, update requests, replication, network changes, and snapshot creation. Future read commands require explicit review and registration. Socket filesystem mount mode is not command authorization.
- Self-tests cover crash recovery of cloned rqlite and application Raft, target migrations, representative API reads, disposable database write/read/delete operations, UI asset loading, sidecar read/write behavior within the sandbox, restricted IPC reads, and persistence after a test-stack restart. Check that the expected release is running. Expected denials of external operations must not be mistaken for successful tests of those operations.
- If preflight fails because its running snapshot cannot recover, report that failure without stopping production. Do not silently fall back to testing empty state.

## rqlite compatibility policy

Pin an exact rqlite image digest in each management release bundle, alongside its other service images. Extend release metadata with the tested rqlite source-version range and validate the live version before preflight. Release CI must exercise every advertised transition class and any required intermediate upgrade. Do not infer support from major-version numbers or from a passing liveness endpoint.

There are three distinct contracts:

| Contract | Required evidence |
| --- | --- |
| Management API/SQL and target rqlite | Application integration tests against the pinned target image. |
| Existing rqlite data and target rqlite | Upstream-supported path, release CI, and successful recovery/migrations/restart on the appliance's cloned data. |
| Rollback to previous rqlite | Previous image plus the complete pre-upgrade data-directory snapshot, including its Raft state and SQLite/WAL files. |

Rollback never starts an old rqlite image against a data directory modified by a newer release. Unsupported direct transitions are rejected before production stops; publish an intermediate management release when needed. Upgrades needing logical export/import require a separately implemented and tested migration path rather than an unplanned fallback.

Upstream examples checked on 2026-10-20: v9 recommends upgrading through 8.43.4 first; v10 recommends the latest v9 before upgrading, prohibits joining a new v10 node to a pre-v10 cluster, changes some command-line flags, and documents backup/redeployment for downgrading. Consult the [official rqlite changelog](https://github.com/rqlite/rqlite/blob/master/CHANGELOG.md) for each target, and its [backup and restore guidance](https://rqlite.io/docs/guides/backup/).

A replicated rqlite deployment cannot use local snapshot rollback as cluster rollback: peers can replicate newer committed state back into the restored member. Detect and reject that topology in this first implementation. The test stack must never join production consensus groups.

## Interfaces and operator behavior

- Add full-access hostd IPC commands for release discovery, prepare/start, status, rollback, and cleanup. Mutation commands accept an operation/idempotency identifier, selected release, and actor; they return an operation ID before long-running work. The read-only preflight socket exposes none of these commands.
- Add administrator-authenticated management update endpoints in OpenAPI, client methods, shared CLI commands, and a basic UI for release selection, progress, results, rollback, and cleanup. Hostd remains the authoritative operation store; any rqlite operation record is a projection that can be rebuilt after restoration.
- Keep a root host recovery command backed by hostd IPC for status and rollback while the management API is unavailable. The web UI and remote CLI report the maintenance disconnect and reconnect/poll when management returns; do not promise uninterrupted API availability.
- Preserve API authentication and idempotency conventions. Hostd validates release sources, versions, dataset ownership, and paths independently of the caller; never accept arbitrary shell commands or arbitrary rollback dataset targets.
- Retain the active and one previous committed release with its rollback point. Delete older updater-owned snapshots and images only after success, after checking references from OS trials and retained releases. Preserve resources used by workloads or other Docker projects. Partial cleanup is retryable and does not invalidate a successful update.

## Validation and limits

Acceptance requires real ZFS/Docker appliance tests, not only mocked commands:

- Preflight passes without changing production data or affecting workloads; failed tests leave production running.
- The initial sandbox caps each of its four services at 1 GiB and checks for at least 4.5 GiB `MemAvailable` before cloning, preserving 512 MiB of additional host headroom. Lower-memory appliances reject preflight rather than risk production OOM; reducing this requirement needs measured sidecar recovery memory tests.
- Network probes cannot reach host services, production containers, other nodes, internet endpoints, or external DNS over either address family. Approved sandbox service traffic succeeds.
- Every mutation IPC command is rejected on the restricted socket, including newly registered production commands; audited reads succeed.
- Writes made during preflight are present after the real update because the final snapshot is taken later.
- Inject startup, migration, sidecar, and final readiness failures. Verify all datasets and service definitions restore together and the old stack accepts reads and writes again.
- Kill hostd and reboot the appliance at each production checkpoint, including partial restore; verify deterministic recovery before core-stack startup.
- Restart Docker and cut appliance power after candidate containers have run for more than ten seconds but before commitment. Verify no management container starts before reconciliation authorizes it, restored data has no competing writers, and workload restart policies are unchanged. Repeat during rollback, partial policy suppression, partial policy restoration, and an uncommitted OS trial.
- Exercise each advertised rqlite transition and the old-image/old-snapshot rollback, including application Raft state and sidecar persistence.
- Test insufficient space, excessive test resource usage, busy datasets, unknown snapshots/clones, repeated requests, concurrent mode changes, and reference-safe cleanup.

Rollback intentionally discards management configuration, metrics, and logs written after the final snapshot. It cannot undo external workload mutations performed after activation. Trials may accept normal changes once readiness passes; report resulting workload/configuration differences on rollback and do not automatically delete or recreate workloads to conceal them.

Release artifacts are trusted from the configured official HTTPS source, with hashes and digest checks; signed release manifests are deferred. This plan does not implement cluster rollout coordination or validate a new OS kernel.
