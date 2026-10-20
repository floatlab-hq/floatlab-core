# Management backend, UI and CLI changelog

## Unreleased

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
