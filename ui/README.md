# FloatLab UI

The live Vue dashboard and component galleries live in the core pnpm workspace.

Run `pnpm install` from the repository root, then `pnpm --dir ui dev`.
Sign in with the backend administrator credentials; bearer tokens remain in memory
and are cleared on reload or expiry. Vite proxies /api to http://localhost:8080.
Set VITE_API_PROXY_TARGET in ui/.env.local for another backend. Production hosting
must proxy /api to the control plane.

The overview shows hosts, apps, notifications and available resource metrics.
Inspect a host for configuration, associated apps, storage and recent logs.
Refresh runs every 30 seconds while visible. Unavailable data is labelled.
Management workflows remain accessible through the command palette (Alt/Ctrl/Cmd+/),
including completion, confirmation, prompts and Compose editing.

## Contracts and checks

`pnpm --filter @floatlab/client generate:api` generates TypeScript from the root
OpenAPI contract. `pnpm --dir ui forms:sync` generates form metadata from the same
local specifications. Commit both outputs; CI rejects drift.

Run `pnpm --dir ui test`, `pnpm --dir ui typecheck`, `pnpm --dir ui lint`
and `pnpm --dir ui build`. Tests run once by default.

## Component galleries

`pnpm --dir ui storybook` starts Storybook; `storybook:build` writes
storybook-static/. `story:dev` and `story:build` retain Histoire.
Both galleries use fixtures and never require live infrastructure credentials.
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
