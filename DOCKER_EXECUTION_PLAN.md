# Docker Stack Execution API Plan

## Summary

Build a new `floatlab-api` Go binary in this repository. It will use the official Docker Compose v5 SDK and Docker Engine API through the bind-mounted `/var/run/docker.sock`, while privileged ZFS and host-veth operations remain behind the existing agent Unix socket.

Stack operations are local-node, asynchronous, resumable, admin-only, and persisted in rqlite. Source Compose projects live under `floatlab/<stack>/`, with `docker-compose.yml` remaining the source of truth.

## Public API

All mutation endpoints require `Authorization: Bearer <JWT>` and `Idempotency-Key`. Validate issuer, audience, signature, `roles` containing `admin`, and record `sub` as the event actor.

- `POST /api/v1/stacks/{name}/start`: optional `application/yaml` Compose body or streamed tar project archive; slugify `name`, persist it as immutable Compose `name`, and replace existing configuration when supplied.
- `POST /api/v1/stacks/{id}/restart`
- `POST /api/v1/stacks/{id}/stop`
- `DELETE /api/v1/stacks/{id}?purge=false`: Compose down, release networking, and remove active metadata while preserving ZFS/config; `purge=true` also destroys the stack dataset.
- `POST /api/v1/stacks/{id}/snapshots`: create a recursive whole-stack snapshot.
- `POST /api/v1/stacks/{id}/restore`: body contains `snapshotId`; preserve current/newer state through clone/swap and restart only when previously running.
- `POST /api/v1/stacks/{id}/upgrade`: body `{ "images": { "service": "repository:tag" } }`; reject unknown services.
- `GET /api/v1/operations/{operationId}`: return action, checkpoint, pending/running/succeeded/failed state, timestamps, and error.
- `GET /api/v1/stacks/{id}/status`: return stored/lifecycle state, active operation, stack IP, and container ID/service/image/state/health/exit status.
- `GET /api/v1/stacks/{id}/config`: return the canonical source `docker-compose.yml`, including `x-fl-*` options.
- `GET|DELETE /api/v1/stacks/{id}/snapshots/{snapshotId?}`: list snapshots/recovery points or explicitly remove one.
- `GET /api/v1/stacks/{id}/alerts`: return Compose-defined rules joined with current rqlite status.
- `GET /api/v1/stacks/{id}/events?after=<cursor>&limit=<n>`: stable cursor pagination over one year of history.
- `GET /api/v1/stacks/{id}/containers/{containerId}/terminal`: WebSocket Docker exec attach, default `/bin/sh`, optional command arguments, TTY resize messages, and membership validation.
- Add admin CRUD under `/api/v1/settings/network-pools` for named IPv4 CIDR/start/end pools and one default pool.
- Add an authenticated internal alert-transition endpoint accepting rule ID, pending/firing/resolved state, observed value, and timestamps.
- Mutations return `202` plus `{operationId, stackId, status}` and `Location`; concurrent operations on one stack return `409`. Repeated idempotency keys return the original operation.

## Compose Extensions And Rewriting

Use flat extensions, relying on Compose's specified behavior of preserving and ignoring `x-*` fields. Pin the official Compose v5 SDK rather than invoking the CLI.

- Top-level: `x-fl-network-pool`, `x-fl-health-timeout`, and `x-fl-alert-rules`.
- Each long-syntax writable service mount may define `x-fl-recordsize`, `x-fl-compression`, `x-fl-quota`, and `x-fl-snapshots`.
- `x-fl-snapshots` is a whitespace-separated list of positive `<interval>/<retain>` tokens using `m`, `h`, `d`, `w`, `mo`, or `y`; malformed and duplicate tiers are invalid.
- Defaults are `recordsize=32K`, `compression=lz4`, unlimited quota, no scheduled snapshots, and a two-minute health timeout unless overridden.
- Validate recordsize as a ZFS-supported power-of-two size, compression against an allowlist, quota as a valid ZFS size or unlimited value, and snapshot counts and intervals before provisioning.
- The first YAML occurrence of a shared mount defines its options. Later explicit options must match; conflicts are syntax errors.
- Writable relative bind `./mysql-data` maps to `floatlab/<stack>/mysql-data`; named volume `mysql-data` uses the same direct child naming rule. Reject collisions, traversal, writable absolute binds, and writable external volumes.
- Stream project archives directly into staging under the stack dataset, limited only by available filesystem space. Reject absolute paths, `..`, escaping links, special device entries, and duplicate conflicting paths; clean staging after failure.
- Permit full Compose features when referenced local files exist in the uploaded project. Import writable bind contents into their new child datasets before runtime rewriting.
- Persist only the canonical source project. Build a separate in-memory runtime project that replaces writable sources with ZFS mountpoints and overrides every published port's Docker `HostIP` with the stack IP.
- Reject `network_mode: host`. `expose` alone does not allocate an IP.
- If `ports` exist, inject one project-local Docker bridge and attach only services publishing ports. Projects without published ports receive neither the injected bridge nor a stack IP.

## Storage, Networking, And Durable State

Extend the host agent with narrowly validated operations rather than granting `CAP_NET_ADMIN` to the API container.

- Add ZFS property update, recursive snapshot, clone, rename, promote, and recovery-tree deletion operations. Keep shell-free `exec.CommandContext` behavior and the existing ZFS validation and error mapping.
- Add host-network operations to create, inspect, and delete a deterministic veth pair, assign the allocated IPv4 prefix to the host end, bring both ends up, and attach the peer to existing `floatlab-lan`.
- Validate `floatlab-lan` and the `ip` utility during agent setup. Interface creation and deletion must be idempotent and must never modify unrelated links.
- Allocate the lowest free address transactionally from the selected or default rqlite pool. Prevent overlapping pools, network or broadcast allocation, pool shrinking or deletion around active allocations, and duplicate `(pool,address)` rows.
- Reserve an address as pending, create the veth, then transactionally activate the allocation and append `address.allocated` to an outbox. Compensate by deleting the interface and releasing the reservation if final persistence fails.
- Add rqlite migrations for stack metadata, operation checkpoints, idempotency keys, IP pools and allocations, events, alert rules and status, snapshot and recovery records, scheduler state, and the DNS outbox.
- Use rqlite's parameterized HTTP API and transactional batch writes; do not use queued writes for lifecycle state.
- Add a single-node rqlite service backed by `floatlab/system/rqlite`; mount `/var/run/docker.sock`, the agent socket, and `/floatlab` into `floatlab-api`. Keep the existing agent Docker proxy for compatibility.
- Run daily cleanup of completed operations and events older than 365 days. DNS outbox records remain until a future consumer acknowledges them.

## Lifecycle Workflows

Implement each operation as persisted, named checkpoints. On API restart, reload pending and running operations, inspect Docker, ZFS, and network state, skip completed idempotent steps, and resume.

- Start: load the supplied or stored project, validate and extract it, create or update stack and mount datasets, import bind data, synchronize alert rules, allocate networking when needed, rewrite the runtime Compose model, pull or build, call Compose `Up`, wait for running and healthy state, then emit `Created` when infrastructure was newly created and `Start`.
- Restart and stop: call Compose SDK operations, wait for affected containers, and emit one result event containing all affected container IDs and services.
- Delete: Compose down, delete the veth, release the IP, remove active rqlite stack state, retain events, and preserve the ZFS project unless `purge=true`.
- Snapshot: generate a stable operation-derived name and recursively snapshot the stack root. Scheduled snapshots run on local wall-clock boundaries; overlapping tiers create separate snapshots and retain only the newest configured count in each tier.
- Restore: stop if running, snapshot current state, clone every dataset from the selected recursive snapshot into a deterministic temporary hierarchy, rename the current tree into a recovery location, activate and promote the clones, reload its Compose config, and restart only if previously running. Keep the recovery tree until explicit deletion.
- Upgrade: validate and pull all requested images, create a pre-upgrade recursive snapshot, patch only matching service image nodes in the source YAML, run Compose reconciliation, and wait up to the stack health timeout. On failure, restore the prior config and ZFS state through the same clone and swap workflow, restart, and report failed-but-rolled-back.
- Pause scheduled snapshots while another stack operation is active so upgrade and restore checkpoints remain deterministic.
- Subscribe to Docker events filtered by Compose project labels. Record `Crashed` for an unplanned container `die`, including container, service, image, and exit status, while suppressing events caused by known stop, restart, and delete operations.

## Alerts And Events

`x-fl-alert-rules` contains unique rule names with metric, optional service or mount selector, comparator, numeric threshold, duration, severity, and message.

Supported v1 metrics are container CPU percent, memory percent, restart count, managed-volume used percent, and free bytes. Import rules into rqlite on configuration changes; removed rules become inactive rather than erasing history.

Event rows contain ID, UTC timestamp, stack, type, outcome, actor subject or `system`, operation ID, affected containers, and typed detail or error data. Record successful and failed `Created`, `Start`, `Stop`, `Restart`, `Delete`, `Upgrade`, `Snapshot`, and `Restore` operations, plus unsolicited `Crashed` and `Alert` transitions. Alert status and its history event must update in one rqlite transaction.

## Test Plan

- Unit-test slug and name immutability, extension parsing, property bounds, shared-mount conflicts, schedule grammar, calendar boundaries and retention, alert validation, and short, long, range, TCP, and UDP port rewrites.
- Test tar traversal, link and device rejection, streamed ENOSPC cleanup, relative file resolution, writable-bind import, external writable-volume rejection, and source and runtime project separation.
- Test IP pool validation and allocation races, veth command arguments, compensation, interface idempotency, bridge absence, and DNS outbox atomicity.
- Test JWT issuer, audience, role, and subject handling, required idempotency keys, per-stack `409`, cursor pagination, one-year cleanup, and WebSocket container ownership, resize, and disconnect.
- Test every workflow checkpoint by interrupting after each step, restarting the API, and proving it resumes without duplicate resources or events.
- VM acceptance: start a multi-service archived project, verify child datasets and properties, injected bridge and stack-IP port bindings, lifecycle, status and events, scheduled retention, crash capture, terminal attach, soft delete and reload, purge, restore with a preserved recovery tree, successful upgrade, and unhealthy upgrade rollback.
- Run `GOCACHE=/tmp/floatlab-go-cache go test ./...`, OpenAPI validation, and Linux Docker, ZFS, and rqlite integration tests.

## Assumptions And Deferred Work

Linux, ZFS pool `floatlab`, bridge `floatlab-lan`, IPv4 Layer 2 connectivity, one management API per node, and exclusive ownership of configured address pools are prerequisites.

Multi-node routing and Raft execution, DNS consumption, alert evaluation, IPv6, Layer 3 routing, and automatic recovery-point deletion remain outside v1.
