# Management backend, UI and CLI changelog

## Unreleased

- Add alert and alert-rule CLI/palette commands for listing, inspection, timed muting, enabling/disabling, and validated partial JSON edits, with action and resource completion.
- Add an alert silence API and preserve mute deadlines across incident notifications while evaluation and recovery continue.
- Advance update command completions through component, action, and node choices in both the web palette and interactive CLI.

- Use the uuid library for idempotency keys so command, update, backup, and restore actions work over HTTP when crypto.randomUUID is unavailable.

## 0.1.4

- Log previously silent provisioning failures with stack and node identifiers, the failing stage, dataset context where applicable, and the underlying error.
- Identify the failed network provisioning step, including pool selection, address allocation, and host service configuration.

## 0.1.3

- Add partition-tolerant lab management with node-owned workload databases and authenticated, read-only workload replicas on peers.
- Configure multiple backup destinations per managed mount through Compose, the API, CLI and dashboard; share canonical snapshots across local and backup schedules while retaining each destination independently.
- Track replication failures and recovery, expose per-node snapshot protection, and durably clean source holds when a destination or node is removed.
- Restore received backups as separately named stacks on the backup node or another lab node, without requiring the original source to be online. Verify every writable mount before provisioning and starting the new stack; retain existing workloads and backups.
- Offer received-snapshot selection and editable cached Compose configuration. File-backed project assets absent from backups are rejected; local restored clones retain their origin snapshot. Backup source migration during failover remains unsupported.
- Verify real three-node replication and local/remote restore acceptance, plus Go, race, client, CLI and UI checks.

## 0.1.2

- Add administrator update controls in the API, client, CLI, and UI, with hostd-backed progress, rollback, cleanup, and reconnect polling during management downtime.
- Add isolated management self-tests, maintenance fencing, and explicit rqlite compatibility checks. This release supports standalone rqlite 8.26.0; replicated management deployments and unlisted version transitions are rejected.
- Establish management update protocol 1; releases without this protocol require an update-capable baseline before self-update.
- Evaluate alerts with fresh resource-specific evidence, explicit recovery predicates, persistent incident identity, and retained recurrence history.

## 0.1.1

- Publish the management backend, UI and CLI for the first offline installer release.

## 0.1.0

- Establish independent manage releases and embedded version metadata.
- Publish the management image to GHCR and Docker Hub.
- Verify the built container serves the UI, JavaScript, CSS and versioned API before publishing.
- Report advisory host compatibility alerts without interrupting operations.
