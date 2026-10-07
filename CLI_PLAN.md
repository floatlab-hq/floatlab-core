# FloatLab CLI and Web Command Palette Plan

## Status

Proposed implementation plan for a shared TypeScript command engine used by:

- A native `floatlab` command-line executable compiled with Bun.
- A Vue command palette opened with <kbd>Alt</kbd>+<kbd>/</kbd> in the FloatLab web interface.

The Go control plane remains the source of truth for authentication, Compose validation, stack lifecycle, container lifecycle, logs, snapshots, and upgrades.

## Goals

- Provide the same commands, aliases, arguments, completion, and API behavior in the terminal and browser.
- Support interactive terminal use and one-shot commands suitable for scripts.
- Complete commands, stack names, container names, node names, and service names using Tab and an arrow-navigable suggestion list.
- Open Compose configuration in the system editor from the terminal and in a modal editor from the web palette.
- Validate Compose through the Go API before create or edit mutations.
- Keep credentials out of command history and avoid storing passwords.
- Produce standalone Linux and macOS executables without requiring Bun on the target machine.

## Non-goals for the first release

- Windows support.
- Multiple saved FloatLab profiles.
- Offline Compose validation.
- Live log following.
- Snapshot deletion or restore commands.
- A terminal emulator in the browser.
- Monaco, CodeMirror, or another heavyweight browser editor.
- Container deletion.

## Platform decision

Use TypeScript for the shared command engine and Bun for the native executable.

This is preferable to a Go-only CLI now that the command language has two TypeScript consumers. The browser and terminal can share the command registry, aliases, parser, completion providers, generated OpenAPI types, API requests, and output formatting. Only presentation and host integration differ.

Keep pnpm as the repository package manager because the UI already uses it. Add Bun only as the CLI runtime, test runner, and executable compiler. Do not migrate the existing UI dependency workflow to Bun as part of this work.

### Proposed workspace layout

```text
cli/                         Bun entry point and terminal adapter
packages/floatlab-client/    Shared API client and command engine
ui/                          Vue application and web command palette adapter
```

Add a root `pnpm-workspace.yaml` covering these three packages. Move OpenAPI type generation and the `openapi-fetch` dependency into `packages/floatlab-client`; the UI and CLI import the same generated types and client factory.

## Command interface

Running `floatlab` without arguments opens the interactive command prompt. Running it with arguments executes one command and exits.

| Command | Aliases | Behavior |
| --- | --- | --- |
| `create <name> [--primary node] [--secondary node] [--start]` | `new`, `add`, `mk`, `make` | Gather missing values interactively, edit Compose, validate, create, and optionally start. |
| `edit <stack>` | `vi`, `configure` | Download canonical Compose, edit, validate, and submit if changed. |
| `start <stack> [container]` | - | Start the stack, or one container when a container is supplied. |
| `stop <stack> [container]` | `shutdown` | Stop the stack, or one container when a container is supplied. |
| `delete <stack> [--purge]` | `remove`, `rm` | Delete a stack. `--purge` also destroys its dataset and snapshots. |
| `logs <stack> [container] [--search terms] [--since value] [--limit n]` | - | Fetch finite recent or remotely filtered logs. |
| `info <stack> [container]` | `inspect` | Show detailed stack or container information. |
| `list [stack] [--containers]` | `ls` | List stacks, one stack's containers, or detailed containers across all stacks. |
| `upgrade <stack> [service=image ...]` | - | Upgrade selected service images with API rollback behavior. |
| `snapshot create <stack>` | - | Create a recursive whole-stack snapshot. |
| `snapshot list <stack>` | - | List stack snapshots. |

The interactive prompt also supports `help`, `exit`, and `quit`.

### Interactive behavior

- Tab accepts the selected completion.
- Up and Down select suggestions while the completion list is visible and navigate history otherwise.
- Suggestions include a short description, such as stack state or container health.
- Refresh cached completion data after a mutation. Use a short in-memory TTL for changes made outside the current client.
- Never open a login prompt from a completion request. Return static suggestions when dynamic data is unavailable.
- Confirm `start`, `stop`, and `delete` in the terminal prompt and web palette.
- A direct one-shot invocation intentionally does not ask for confirmation.
- When `create --start` is used interactively, confirm only the start phase. Declining leaves the created stack stopped.

### Output behavior

- Interactive terminal `logs` and `info` open formatted output with `less -R`.
- If `less` is unavailable, print the result with a warning.
- Direct invocations write to stdout and errors to stderr.
- The web palette keeps the overlay open and renders tables, logs, errors, and operation progress below the command input.
- Exit non-zero for usage, authentication, API, validation, editor, and operation failures.

## Shared TypeScript design

The shared package exports only browser-safe code. It must not import Bun, Node filesystem APIs, Vue, or terminal libraries.

### Command registry

Define each command once with:

- Canonical name and aliases.
- Positional argument and flag definitions.
- Help text.
- Completion provider.
- Async execution function.

Both adapters pass either `process.argv` tokens or tokenized palette/prompt input into the same dispatcher. Implement a small tokenizer supporting whitespace, single and double quotes, and backslash escaping. Do not implement shell expansion, pipes, redirects, substitutions, or environment interpolation.

### Platform boundary

The command engine receives a minimal runtime object:

```ts
interface CommandRuntime {
  interactive: boolean;
  confirm(message: string): Promise<boolean>;
  editCompose(initial: string): Promise<string | undefined>;
  present(result: CommandResult): Promise<void>;
}
```

`undefined` from `editCompose` means cancellation. Keep authentication and API transport in a shared client configured by each adapter; do not add a generic dependency-injection framework.

Represent output as a small discriminated union for text, tables, logs, and operation progress. The terminal formatter and Vue renderer consume this union.

### Resource resolution

- Accept stack names or UUIDs and resolve them through `GET /stacks`.
- Scope containers beneath their stack: `start plex web`, not a global container lookup.
- Accept exact container name, full ID, or unique ID prefix within the selected stack.
- Obtain upgrade services from the stack's known containers for the first release.
- In interactive upgrade, repeatedly select a service and enter its replacement image until the user submits.
- In direct upgrade, require at least one `service=image` argument.

## Terminal adapter

Use `@inquirer/core` and focused prompts from `@inquirer/prompts` for the command input, selections, confirmations, and masked passwords. Build one small custom command-input prompt around the shared completion provider; do not adopt a full-screen TUI framework.

### Connection and session

- Read `FLOATLAB_URL` and `FLOATLAB_TOKEN` first.
- Otherwise read `$XDG_CONFIG_HOME/floatlab/config.json`, falling back to `~/.config/floatlab/config.json`.
- Create the directory with mode `0700` and configuration with mode `0600`.
- Store only the normalized API URL, access token, and expiry.
- Never store the username or password.
- Accept either an origin URL or a URL ending in `/api/v1`; append `/api/v1` when the supplied URL has no path.
- If the URL is missing on an interactive terminal, prompt and save it.
- If the saved token is missing or expired, prompt for username and a masked password and call `/auth/token`.
- In a non-TTY invocation, fail with an actionable message when environment credentials or a valid saved session are unavailable.
- If an environment token receives `401`, return the error without changing saved state. If a saved token receives `401`, clear it and permit one interactive login retry.

### Editor and pager

- Resolve the editor from `$VISUAL`, `$EDITOR`, then `vi`.
- Write Compose to a mode-`0600` temporary `.yaml` file and delete it when finished.
- A non-zero editor exit cancels the operation.
- When validation fails, print the API error and offer to reopen the same temporary file or cancel.
- Skip the edit mutation when content is unchanged.
- Spawn `less -R` with inherited terminal input/output and stream formatted content to its stdin.

### Packaging

Compile release binaries with `bun build --compile` for:

- Linux x64 baseline.
- Linux arm64.
- macOS x64 baseline.
- macOS arm64.

Embed the application version at build time. Add Bun to the Nix development shell and CI environment, but do not require Bun on end-user machines.

## Web command palette

- Register Alt+/ globally using `KeyboardEvent.code === "Slash"` and `altKey` so keyboard layout transformations do not break the shortcut.
- Render the palette as an accessible dialog containing a combobox and listbox.
- Move active selection with Up/Down, accept with Tab, execute with Enter, and close with Escape.
- Trap focus while open and restore focus to the previous element on close.
- Do not reopen the palette while the Compose editor or confirmation dialog is active.
- Use the web application's authenticated API client; the palette does not store a separate token.
- Use a native `<textarea>` in a modal for Compose editing. Validate on Save, display errors next to the editor, and keep the editor open after validation failure.
- Show lifecycle confirmation in a modal and return its answer through the shared runtime interface.
- Render results in the palette panel with appropriate table markup and a scrollable preformatted log view.

## Public API changes

### Compose validation

Add authenticated `POST /stacks/validate` outside mutation idempotency middleware.

Request:

```json
{
  "name": "plex",
  "compose_file": "name: plex\n...",
  "stack_id": "optional-existing-stack-uuid"
}
```

- For create validation, `name` is required.
- For edit validation, `stack_id` selects the existing stack and its canonical name.
- Run `compose.CanonicalSource`, `compose.ParseAndValidate`, and `compose.ParseLifecycle`.
- For edit validation, also reject changes to primary and secondary node assignments.
- Return `204 No Content` when valid.
- Return the existing JSON error shape with `400 Bad Request` and code `compose_validation` when invalid.
- Do not return normalized YAML or parsed metadata.

The create and update mutations must repeat validation because they remain the trust boundary.

### Simplified stack creation

Change `StackCreate` so only `name` and `compose_file` are required.

- Canonicalize and validate the document in `POST /stacks`.
- Derive primary node, secondary node, failover mode, and automatic failover duration from `x-fl-stack`.
- Store the canonical Compose content.
- Continue accepting the old duplicated request fields during the current API version. When supplied, reject a mismatch with the Compose values.
- Mark those duplicated fields deprecated in OpenAPI and remove them only in a future API version.

### Container lifecycle

Add:

```text
POST /stacks/{id}/containers/{containerId}/start
POST /stacks/{id}/containers/{containerId}/stop
```

- Require administrator JWT and `Idempotency-Key`.
- Require the parent stack to be running on its primary or backup node.
- Resolve the active node from stack lifecycle state.
- Verify the requested container belongs to the stack before dispatching.
- Add matching `docker.start` and `docker.stop` IPC actions and thin Docker client methods.
- Treat already-running start and already-stopped stop as successful idempotent operations.
- Return the updated documented `Container` representation.
- Do not change the stack lifecycle state for an individual container operation.

### Logs contract conformance

Make handlers honor the existing OpenAPI contract:

- Stack logs: `tail`, `since`, and `service`.
- Container logs: `tail`, `since`, and existing `follow` compatibility, although the CLI does not expose follow in v1.
- Node logs: `tail`, `since`, and `unit`.
- Search: `query`, `start`, `end`, and `limit`.
- Enforce documented limits and return `400` for invalid timestamps, ranges, limits, or LogsQL.
- Return the documented `LogLine` field names consistently for JSON and SSE.

Regenerate the shared OpenAPI TypeScript schema after all contract changes.

## Command workflows

### Create

1. Resolve or prompt for stack name and node assignments.
2. Generate a minimal Compose skeleton containing the name, selected nodes, storage pool, and an empty `services` map.
3. Open the platform editor.
4. Call `/stacks/validate`; reopen or cancel after an error.
5. Submit only `name` and `compose_file` to `POST /stacks`.
6. If `--start` is absent, report the created stack.
7. If `--start` is present, wait up to five minutes for the stack to reach `Idle`, respecting cancellation.
8. Confirm in interactive mode and call the start endpoint.
9. If provisioning or start fails, clearly report that creation succeeded but start did not.

### Edit

1. Resolve the stack and fetch `/stacks/{id}/config`.
2. Open the editor and skip if unchanged.
3. Validate with the stack ID.
4. Submit the validated content to `/stacks/{id}/compose`.

### Start, stop, and delete

1. Resolve the stack and optional container.
2. Show the exact action and resource in interactive confirmation.
3. Send a UUID `Idempotency-Key` with the mutation.
4. Print the accepted or updated state.

### Logs and info

1. Resolve the stack and optional container.
2. Use the scoped recent-log endpoint without `--search`, or `/logs/search` with a safely escaped resource scope and search terms.
3. Return a structured log result for the adapter to page or render.
4. Build stack info from stack detail and status; build container info from the stack container response.

### List

- `list`: one row per stack with name, state, primary node, and secondary node.
- `list <stack>`: service, container, image, state, health, and active node.
- `list --containers`: fetch each stack's containers and render the same detailed columns with an additional stack column.

### Upgrade and snapshot

- Validate that every upgrade service exists and every replacement image is non-empty before mutation.
- Do not add a second client-side rollback workflow; report the server operation state.
- `snapshot create` uses the existing whole-stack snapshot endpoint and idempotency key.
- `snapshot list` displays snapshot name, creation time, type, and space usage when returned by the API.

## Implementation checklist

### Phase 1: API foundation

- [ ] Add `/stacks/validate` to OpenAPI.
- [ ] Implement Compose validation handler and edit invariants.
- [ ] Simplify `StackCreate` requirements and deprecate duplicated fields.
- [ ] Update stack creation to canonicalize, validate, and derive configuration server-side.
- [ ] Add container start/stop operations to OpenAPI.
- [ ] Add Docker client start/stop methods.
- [ ] Add IPC payloads and `docker.start`/`docker.stop` handlers.
- [ ] Add public container lifecycle handlers with ownership and active-node checks.
- [ ] Bring logs handlers and response fields into OpenAPI conformance.
- [ ] Add Go handler, IPC, and Docker tests.
- [ ] Run OpenAPI lint and regenerate TypeScript schema.

### Phase 2: TypeScript workspace and shared engine

- [ ] Add the pnpm workspace and shared package.
- [ ] Move generated OpenAPI types and API client factory into the shared package.
- [ ] Implement tokenizer and command/flag parsing.
- [ ] Implement command registry, aliases, help, and error handling.
- [ ] Implement resource resolution and completion providers.
- [ ] Implement shared result types and format-neutral command handlers.
- [ ] Implement create/edit validation workflows.
- [ ] Implement lifecycle, logs, info, list, upgrade, and snapshot handlers.
- [ ] Add shared unit tests using fake API and runtime adapters.

### Phase 3: Bun terminal application

- [ ] Add CLI package, executable entry point, and direct argument execution.
- [ ] Implement the interactive Inquirer command prompt and history.
- [ ] Implement URL/session loading, login, expiry, and file permissions.
- [ ] Implement system editor and validation retry loop.
- [ ] Implement `less` paging and direct stdout/stderr formatting.
- [ ] Add terminal adapter and filesystem/process tests.
- [ ] Add Bun to Nix and CI.
- [ ] Build and smoke-test all four release targets.

### Phase 4: Vue command palette

- [ ] Add the global Alt+/ shortcut and palette dialog.
- [ ] Implement combobox/listbox completion and keyboard behavior.
- [ ] Connect the palette to the shared command engine and authenticated API client.
- [ ] Implement confirmation and Compose editor modals.
- [ ] Implement text, table, log, error, and progress result components.
- [ ] Add accessibility and keyboard interaction tests.
- [ ] Verify responsive behavior at narrow and wide viewport sizes.

### Phase 5: Integration and release

- [ ] Exercise terminal and browser commands against the development VM.
- [ ] Verify aliases and completion against real stacks and containers.
- [ ] Verify create/edit validation prevents every invalid mutation tested.
- [ ] Verify interactive confirmations and direct non-interactive behavior.
- [ ] Verify session expiry and login recovery.
- [ ] Add CLI installation and web shortcut documentation.
- [ ] Add release artifacts and checksums to the release workflow.

## Test plan

### Go API tests

| Area | Required cases |
| --- | --- |
| Validation | Valid create returns 204; invalid YAML, missing extensions, invalid lifecycle data, and changed edit nodes return 400; no state is mutated. |
| Create | Minimal request succeeds; values are derived from Compose; canonical content is stored; matching legacy fields work; mismatches fail. |
| Container lifecycle | Correct active-node dispatch; foreign and missing containers fail; idle stack fails; start/stop are idempotent; host errors propagate. |
| Logs | Every documented query parameter is applied; defaults and maximums work; invalid values fail; JSON/SSE use documented fields. |
| Idempotency/auth | Validation does not require an idempotency key; mutations do; unauthenticated requests fail. |

### Shared TypeScript tests

| Area | Required cases |
| --- | --- |
| Tokenizer | Whitespace, quotes, escapes, empty quotes, unterminated quotes, and unsupported shell operators. |
| Commands | Canonical names, every alias, missing/excess arguments, flags, help, and exit codes. |
| Completion | Commands, aliases, stacks, nested containers, nodes, services, API failure, cache refresh, and no auth prompting. |
| Workflows | Create, edit unchanged, validation retry/cancel, partial create/start failure, confirmations, log scoping, list modes, upgrades, and snapshots. |
| API errors | 400, 401, 404, 409, 429, 5xx, malformed responses, timeout, and cancellation. |

### Terminal tests

- Environment credentials override saved configuration.
- Missing interactive URL is prompted and saved.
- Config directory/file modes are `0700`/`0600`.
- Password input is masked and never persisted or logged.
- Expired saved sessions reauthenticate once; environment-token failures do not modify disk.
- Editor selection order and non-zero exit behavior are correct.
- Invalid edits reopen the same temporary file and never mutate remotely.
- Interactive results invoke `less -R`; direct results remain pipeable.
- SIGINT cancels prompts, polling, and API requests without corrupting the terminal.

### Vue tests

- Alt+/ opens the palette and focuses the input.
- Escape closes and restores prior focus.
- Up/Down/Tab/Enter operate the completion list correctly.
- Suggestions expose correct ARIA combobox/listbox state.
- Confirm cancellation sends no mutation.
- Compose validation errors retain the document and focus the editor.
- Successful commands render the expected result type.
- Long logs and tables scroll within the overlay without moving the underlying page.

### Integration scenarios

1. Log in, create a valid stopped stack, and see it in both terminal and web `list`.
2. Attempt invalid create and edit documents and confirm neither changes server state.
3. Start and stop a stack from each surface and verify state transitions.
4. Stop and start one container while its parent stack remains running.
5. Search stack and container logs and verify scoping and limits.
6. Upgrade one service and observe the operation result.
7. Create and list a snapshot.
8. Expire the CLI token and verify interactive recovery and non-interactive failure behavior.
9. Execute destructive commands interactively and directly to verify the intentional confirmation difference.

### Required checks

```sh
go test -race -count=1 ./...
pnpm --recursive test
pnpm --dir ui build
pnpm --dir ui generate:api
bun build --compile cli/src/main.ts --outfile /tmp/floatlab
/tmp/floatlab --help
```

CI must also lint the OpenAPI document and build each supported release target before publishing artifacts.

## Acceptance criteria

- Terminal and web use the same command registry and produce equivalent API requests.
- Every requested command and alias is available in both surfaces.
- Completion works for commands and live resource names with Tab and arrow navigation.
- Create and edit never submit before successful Go API validation.
- Interactive terminal editing uses the system editor; web editing uses the modal editor.
- Interactive terminal logs/info use `less`; web logs/info render inside the palette.
- Stack and container lifecycle operations enforce resource ownership and active-node routing.
- Credentials and passwords do not appear in command history, logs, or world-readable files.
- Linux and macOS release binaries run without Bun installed.
- All required automated and integration checks pass.
