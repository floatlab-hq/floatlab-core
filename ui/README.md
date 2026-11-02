# FloatLab UI

The live Vue dashboard and component gallery live in the core pnpm workspace.

Run `pnpm install` from the repository root, then `pnpm --dir ui dev`.
Sign in with the backend administrator credentials; bearer tokens remain in memory
and are cleared on reload or expiry. Vite proxies /api to http://localhost:8080.
Set VITE_API_PROXY_TARGET in ui/.env.local for another backend. Production hosting
must proxy /api to the control plane.

The overview shows hosts, apps, notifications and available resource metrics.
Inspect a host for configuration, associated apps, storage and recent logs.
Refresh runs every 30 seconds while visible. Unavailable data is labelled.
Recent events combine alert notifications with durable workload audit history,
newest first, capped at 50 entries. Command completion also refreshes this feed.
Management workflows remain accessible through the command palette (Alt/Ctrl/Cmd+/),
including completion, confirmation, prompts and Compose editing.

The live dashboard reuses the Storybook navigation, host/app directories and detail
panel. App state and Tools open the workload panel: start/stop, logs, metrics,
Compose, inspection, snapshots, image upgrades and deletion use the shared CLI
command engine. Containers expose start/stop, logs, inspection and command
execution. Cached peer entries expose read operations only. Restart, app links,
network editing, column selection and filters are not exposed without a wired
implementation.

**Add app** collects the name, primary node, optional standby and start preference,
then opens the CLI's validated Compose editor. **Host** prepares `lab invite` in
the command palette; it does not register a fictitious node. **Settings** opens
the implemented alert-rule list; rule edits and other lab/backup operations remain
available through the command palette.

Storybook's **Live Dashboard** includes interactive empty, stopped, running,
creation, workload-action, and cached-peer views. **Create App** and **Workload
Detail** document their individual states. These use isolated in-memory fixtures;
they do not contact or mutate a real appliance.

## Contracts and checks

`pnpm --filter @floatlab/client generate:api` generates TypeScript from the root
OpenAPI contract. `pnpm --dir ui forms:sync` generates form metadata from the same
local specifications. Commit both outputs; CI rejects drift.

Run `pnpm --dir ui test`, `pnpm --dir ui typecheck`, `pnpm --dir ui lint`
and `pnpm --dir ui build`. Tests run once by default.

## Component gallery

`pnpm --dir ui storybook` starts Storybook; `storybook:build` writes
storybook-static/.
The gallery uses fixtures and never require live infrastructure credentials.
Themes, fonts and original component documentation are preserved.

## Publish Storybook locally

Authenticate with `gh auth login`, then run `pnpm --dir ui storybook:publish`.
This builds with the /floatlab-ui/ asset base and publishes generated files to
floatlab-hq/floatlab-ui's gh-pages branch using your GitHub session. It disables the
retired source publishing workflow and configures branch-based Pages hosting.
Your account needs repository write and Pages administration access.
No token is stored in this project and no cross-repository CI secret is required.

Gallery URL: https://floatlab-hq.github.io/floatlab-ui/

See [UPSTREAM.md](UPSTREAM.md) for source provenance and [LICENSE](LICENSE).
