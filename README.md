FloatLab Core
===

FloatLab Core is the brain behind running your FloatLab. It contains several core components that are used
to orchestrate your workloads.

## CLI

Download the matching `lab` binary from a `manage-vX.Y.Z` release, mark it executable, and put it on your `PATH`:

```bash
chmod +x lab-linux-x64
mv lab-linux-x64 ~/.local/bin/lab
```

Build a standalone binary from source with `pnpm build:cli` (Bun required); the output is `dist/lab` and needs no runtime installed.

Set `FLOATLAB_URL` and `FLOATLAB_TOKEN` for scripts. Otherwise run `lab` in a terminal; it asks for the URL and credentials, then stores only the URL, token, and expiry in `~/.config/floatlab/config.json` (or `$XDG_CONFIG_HOME/floatlab/config.json`). Direct commands do not ask for lifecycle confirmation; interactive commands do.

```bash
lab list
lab logs plex --limit 100
lab                 # interactive prompt
```

Direct API commands accept `--body` for inline JSON or text and `--body-file` for raw file contents. Use files for large or binary requests, for example `lab api stacks startStack --path-id STACK_ID --content-type application/x-tar --body-file stack.tar`. JSON files work with `--body-file` too. The two body options are mutually exclusive.

The web interface exposes the same commands through the command palette: press <kbd>Alt</kbd>+<kbd>/</kbd>.

## Installer development

See the [installer developer guide](floatlab-appliance-image/DEVELOPMENT.md) for
running the TUI in mock mode with Go, choosing test scenarios, and running checks.

## Iterating on an installed appliance

Appliances built with the development-mode support can switch the management
container and native host daemon without rebuilding the OS. Earlier appliances
need one image update to install the systemd launcher, watcher and mode command.
The mode switch is a root host command; it persists across reboots.

Build from the repository root, then load the image on the appliance:

```bash
docker build -f docker/floatlab-dev.Dockerfile -t floatlab-dev:local .
docker save floatlab-dev:local | ssh root@FLOATLAB_HOST docker load
ssh root@FLOATLAB_HOST floatlab-mode dev
```

Alternatively, build directly on the host or pull a published dev image and tag
it `floatlab-dev:local`. The build includes the current source tree, Go, Node,
pnpm, Air, Git, GitHub CLI, code-server (headless VS Code), Codex and Claude Code.
It fetches upstream Git history so commits made on the appliance can become PRs.
`SOURCE_REPOSITORY` and `SOURCE_REF` build arguments select a fork or base branch.
The image contains any uncommitted source changes present at build time; review
the build context before sharing it. Tool versions can be selected with
`CODEX_VERSION`, `CLAUDE_VERSION` and `CODE_SERVER_VERSION` build arguments.

The management UI stays on port 8080, now served by Vite with hot module reload;
Go runs behind it on container loopback port 8081 and Air rebuilds/restarts it
when Go source changes. API authentication still uses the appliance credentials.
The dashboard displays **Development mode** from the backend's reported mode.
The installed identity, encryption key, database and Raft data remain in use.

Source is seeded once into `/floatlab/system/dev/workspace`. The whole tool home
is mounted from `/floatlab/system/dev/home` with mode 0700, retaining GitHub,
Codex, Claude and editor credentials/configuration across container replacement
and reboot. Rebuilding the image does not overwrite an existing workspace;
use Git in that workspace to update its source. Failed Go builds are logged and
leave the last successful control-plane process running.

Open the editor through an SSH tunnel:

```bash
ssh -N -L 9090:127.0.0.1:9090 root@FLOATLAB_HOST
# Open http://localhost:9090 in your browser.
# On the appliance, retrieve the generated editor password:
cat /floatlab/system/dev/home/.config/code-server/config.yaml
```

Use the editor terminal, or enter the dev container from the appliance:

```bash
docker compose -f /floatlab/system/docker-compose.yml exec floatlab-control bash
gh auth login --hostname github.com --git-protocol https --web
gh auth setup-git
git config --global user.name 'Your Name'
git config --global user.email 'YOUR_GITHUB_EMAIL'
codex login --device-auth
claude auth login
```

GitHub's browser/device flow and [Codex device authentication](https://developers.openai.com/codex/auth)
work from a remote terminal. [code-server](https://coder.com/docs/code-server/guide)
is password protected and its published port binds only to host loopback. The
Vite server accepts IP access and the appliance's configured node name; for
another DNS alias, set `__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS` in the container.
The dev environment has root Docker/host access, so only trusted developers
should access it. Credentials are persistent local files, not encrypted storage.

After editing hostd, run this **inside the dev container**:

```bash
floatlab-dev-hostd
```

It builds a static Linux binary for the container's architecture, then atomically
replaces `/floatlab/system/dev/bin/floatlab-hostd`. The host's systemd path unit
restarts hostd with the host's normal ZFS/Docker/network tools and environment.
A failed build preserves the running binary. Use a native-architecture dev image;
cross-building through an emulated container does not target the host automatically.
Hostd startup networking commands still use the packaged binary; changes to boot
network initialization, systemd units, kernel or host tools require an OS image.

Review and commit selected files before opening a PR:

```bash
git diff
git add PATHS_YOU_REVIEWED
git commit -m 'Describe the change'
floatlab-dev-pr 'Describe the change'
```

The PR helper offers GitHub login if needed, pushes the current branch without
force, and opens a draft PR. It requires a clean working tree and push access to
`origin`; use a fork as origin if needed. It never stages or commits files.

Return to production on the appliance, including recovery from a broken dev build:

```bash
floatlab-mode production
floatlab-mode status
```

This restores packaged hostd and the production Compose image. It retains the
workspace, secrets and dev binary for the next dev session. It does not roll back
database migrations or workload/storage changes made by development code; take
a backup before testing changes to persistent data. A failed mode switch reports
an error and leaves the requested mode selected; rerun it or select production.

Validation: `python3 scripts/dev-workflow.test.py` checks isolated mode/deployment
and PR behavior plus Compose merging. After building the image,
`bash scripts/dev-container-smoke.sh` tests the frontend, API, editor, tools, Go
reload and restart persistence using disposable containers and volumes, with no
appliance mounts or published ports.

## Alert commands

The interactive CLI and web command palette share these commands:

```text
alerts list active
alerts list silenced
alerts list all
alerts show ALERT_ID
alerts mute ALERT_ID 1h
alert-rules list
alert-rules show RULE_ID
alert-rules disable RULE_ID
alert-rules enable RULE_ID
alert-rules edit RULE_ID '{"condition":"cpu_usage_percent > 95","for_duration":"5m"}'
```

Alert and rule IDs support unique prefixes; rules can also be selected by their
quoted name. Tab completes actions, states, IDs, and example mute durations.
Rule listings include enabled status and per-resource evaluation states;
`alert-rules show` includes labels, observations, freshness, and evaluation errors.
Use `alerts list all` to see active, muted, and resolved incidents with their rule IDs.

Muting accepts positive whole minutes, hours, or days (`30m`, `1h`, `1d`). It
silences the current incident's notifications until that deadline while evaluation
and recovery continue. New notifications for the same incident inherit the mute.
After expiry, an unresolved incident appears active again. A new incident after
recovery has its own identity and is not muted. Disabling a rule stops its
evaluation and resolves its incidents on the next evaluator tick. JSON edits are
partial updates; omitted settings are preserved and the server validates changes.

## Appliance updates

Management and host updates are coordinated by the local host daemon. From the
CLI or web command palette, select a node and an immutable release:

```text
update management releases my-node
update management start my-node manage-v1.2.3
update management status my-node
update host releases my-node
update host prepare my-node host-v1.2.3
update host status my-node
update host activate my-node OPERATION_ID
update host commit my-node OPERATION_ID
```

Add `--upstream owner/repo` to `releases`, management `start`, or host `prepare`
to use another GitHub repository for that command. Management images come from
the selected release manifest; host artifacts come from the selected release.
The default remains `floatlab-hq/floatlab-core`. For example:

```text
update management releases my-node --upstream my-org/floatlab-core
update management start my-node manage-v1.2.3 --upstream my-org/floatlab-core --skip-dry-run
update host prepare my-node host-v1.2.3 --upstream my-org/floatlab-core --skip-dry-run
```

`--skip-dry-run` bypasses the isolated preflight trial for that transaction.
It still validates manifests and compatibility, takes rollback snapshots, and
checks production health. Only use an upstream whose artifacts you trust.
The root `floatlab-hostd update` commands accept the same flags.

Management updates first test an isolated clone, then stop production, take a
fresh rollback snapshot, and upgrade its existing datasets. Failure restores
the snapshot and previous services. Preflight requires 2 GiB of
available memory: four services capped at 384 MiB each, plus 512 MiB of host
headroom. Test containers cannot swap beyond those caps; a candidate that exceeds
its budget fails preflight while production remains running. The default ZFS ARC
budget reserves memory for management and updates at boot; explicitly configured
ARC limits are preserved and still need to leave sufficient available memory.
The API can disconnect during maintenance;
hostd continues the transaction. Host activation reboots the appliance. An OS
trial requires explicit commitment; rebooting an uncommitted trial returns to
the previous OS and restores its management checkpoint.

Use `update management|host rollback my-node OPERATION_ID` for recovery and
`update management|host cleanup my-node OPERATION_ID` for supported cleanup.
Rollback discards post-checkpoint management state and telemetry; it does not
undo changes already made to workloads. A root host recovery command is also
available through `floatlab-hostd update --help` when the API is unavailable.

These paths require an update-capable baseline. Host activation additionally
requires a qualified source/target Docker transition in its release metadata;
unqualified transitions are rejected. Real appliance boot, isolation, and
rollback acceptance remains required before enabling a release for production.
See the [management update plan](MANAGEMENT_UPDATE_PLAN.md) and
[host/OS update plan](HOST_OS_UPDATE_PLAN.md) for recovery rules and acceptance
tests.

## Development VM

VMs use the release installer ISO, private TOML answers with bcrypt password hashes,
and a blank UEFI disk. Host libvirt must be running and accessible to your user.
Export an answers file with Ctrl+E on the installer's Review screen, then run:

```bash
nix develop -c env LIBVIRT_URI=qemu:///system scripts/create-floatlab-vm.sh /path/to/floatlab-answers.toml
```

Review the exact disk IDs in the answers before reuse. The VM disk is
`/dev/disk/by-id/virtio-floatlab-zfs`. Existing domains and volumes are never replaced.
The installer verifies the installation, reboots into the installed OS, and starts
management services. Find its address using the guest agent:

```bash
nix develop -c virsh --connect qemu:///system domifaddr floatlab-dev --source agent
```

The API is at `http://<VM-IP>:8080`; sign in with the credentials used to generate
the answers. To create disposable hosts and run real workload acceptance:

```bash
nix develop "path:$PWD?dir=floatlab-appliance-image" -c env FLOATLAB_INTEGRATION=1 go test -count=1 -timeout 12h -v ./integration
```

The suite generates private answers for each VM, installs through the normal installer,
and requires a completed installation booted from disk before running workloads.
It removes its owned domains, disks, and answers media after the run. It requires KVM;
ordinary CI Go tests skip these appliance scenarios unless explicitly enabled.
See the [unattended installation guide](floatlab-appliance-image/README.md#unattended-installation).

### FloatLab Raft - Distributed Consensus Algorithm

The raft algorithm is used to achieve distributed locking, to ensure that only one application instance
can be running ona single note at one time.

### FloatLab Scheduler - Task Scheduler

This scheduler is used for many core tasks:
- Managing container data replication and backups via ZFS Replication (zfs send/recv)
- Starting and stopping containers on a repeating schedule (like a built-in cron)
- Monitoring the health of containers, restarting if required, and updating scheduler

### FloatLab Control - Management API for Web UI 

This is the main API that is by the Web UI SPA to manage your FloatLab.

### FloatLab Compose  - docker-compose file reader/writer

Contains extended docker-compose file format support, extensions include:

- ZFS Management
  - Snapshot Schedule and Retention Policy
  - Replication Schedules
  - Backup Schedules
  - ZFS Volume Size Limits
- Application Metadata
  - Icon
  - Name
  - HTTP Access URL
- Networking Connection Details

### FloatLab Net - Networking Management

FloatLab needs to manage the host network interface, the ip reservations pool, private internal network, and 
an overlay network

### FloatLab Watch - Monitoring and Alerting

See the [management alert rule catalogue](internal/control/ALERT_RULES.md) for
proposed rules, evaluator behaviours, telemetry gaps and acceptance scenarios.

FloatLab Watch is a component that monitors the health of your nodes, containers, and allows for streaming and 
searching container logs.
Alerts need to cover the following scenarios:
- Disk space thresholds
- zfs pool space thresholds
- zpool status - scrub result
- zpool disk faults
- RAM utilization thresholds
- CPU utilization thresholds
- container replication lag
- container backup lag

### FloatLab Auth - Authentication and Authorization

- Issue JWT Tokens via OIDC/OAuth2
- Role-based access control scopes
- User Management

### FloatLab Config 

All the configuration that needs to be stored and distributed to all nodes in the FloatLab 

The data here will be stored using rqlite or dqlite. See more in the [FloatLab Config](./pkg/config/README.md) package.

### FloatLab Connect

FLoatLab connect provides a stable and secure way for all of your FloatLab nodes to communicate with each other, including
operating the raft protocol, replicating the configuration database, and potentially tunneling container traffic between nodes

### CLI workflow and Caddy end-to-end test

```bash
floatlab create web --file compose.yaml --start
floatlab list
floatlab list web
floatlab config web
floatlab stats web --range 1h
floatlab logs web caddy --limit 100
floatlab exec web caddy -- caddy version
floatlab exec web caddy -- sh -c 'printf hello; printf error >&2; exit 7'
```

File creation uses node assignments in the Compose file; omit `--primary` and `--secondary`. Editor-based creation and editing remain available. Interactive validation failures display the error and ask **“Edit and try again?”**; declining cancels without saving. Direct commands never prompt for a validation retry. Lifecycle requests are asynchronous; check `list` or `info` for completion.

`exec` accepts a container service name, container name, or ID. It runs argv directly, separates stdout and stderr, and returns the command's exit status. Capture is bounded to 60 seconds and 1 MiB combined output. Docker does not provide an exec-kill endpoint: a command can continue inside the container after capture times out or disconnects. Interactive shell attachment and stdin are outside this command's scope.

Install workspace dependencies with `pnpm install --frozen-lockfile`. The CLI workflow is included in the Go integration suite described below; select it with `-args -ginkgo.label-filter=cli-caddy`.

Hostd forwards metrics and logs for FloatLab containers every five seconds to the local VictoriaMetrics and VictoriaLogs endpoints. Set `FLOATLAB_NODE_ID`, `FLOATLAB_VMETRICS_URL`, and `FLOATLAB_VLOGS_URL` in its environment for another deployment. The core Compose files expose these endpoints only on loopback. Log polling currently captures at most 1000 lines and 1 MiB per container per pass; higher-volume workloads need continuous log forwarding.

### Appliance integration suite

All integration appliances run with **3584 MiB RAM (3.5 GiB)**, including instrumented management and host-upgrade candidates. This is the standard test configuration; there is no separate low-memory variant. Existing sandbox memory limits and OOM checks remain enforced.

Install workspace dependencies, ensure the host has KVM and a running libvirt system daemon, then run outside any sandbox that hides `/dev/kvm`:

```bash
nix develop "path:$PWD?dir=floatlab-appliance-image" -c env FLOATLAB_INTEGRATION=1 \
  go test -count=1 -timeout 12h -v ./integration
```

The Ginkgo suite creates disposable UEFI appliances through the pure-Go libvirt RPC client over `/var/run/libvirt/libvirt-sock`. It requires Go, Nix, Python, Docker, Bun, pnpm, xorriso, and image-pull access; neither `virsh`, `virt-install`, libvirt headers, nor C bindings are required by the runner. Docker/rqlite package checks execute inside appliances too. Ordinary `go test` leaves appliance execution disabled.

Select scenarios with `-args -ginkgo.label-filter=multi-node` (or `Lab`, `Replication`, `BackupRestore`, `single-node`, `cli-caddy`, `management-update`, `host-update`, `docker`, `rqlite`). The multi-node table gives each entry fresh appliances and ordered narrative stages. Single-node stages retain their shared appliance. Independent scenarios run serially, and a failed stage skips its dependent stages without retrying it.

The runner builds and reuses instrumented artifacts. Set `FLOATLAB_APPLIANCE_ISO` to reuse an instrumented ISO; ordinary production ISOs fail the coverage preflight. Host-upgrade artifact reuse requires both `FLOATLAB_HOST_TEST_ISO` and `FLOATLAB_HOST_TEST_CANDIDATE`. External `API_URL`, `VM_NAME`, and `VM_SSH` targets are no longer supported.

Reports are written to a unique `floatlab-reports-*` directory under `/tmp`, or under `FLOATLAB_REPORT_DIR`. They include Ginkgo JSON, JUnit XML, guest diagnostics, resource identifiers, endpoint evidence, raw Go coverage, a merged `coverage.out`, and `coverage.html`. Coverage is collected before teardown and checkpointed before planned power interruption. Failed runs retain any coverage that could be collected. Set `FLOATLAB_KEEP_VM=1` to retain owned infrastructure for debugging. SIGKILL or host failure can leave resources behind; use the recorded identifiers for recovery and cleanup.

Ordinary workload setup, payload access, and cleanup use FloatLab's API or CLI. Upgrade persistence proofs use managed stacks; host activation/commit/cleanup and management rollback/cleanup use authenticated update endpoints. Empty IPv6 prefix pools can be deleted with `DELETE /api/v1/network/pools/{id}`; resolved alerts and their linked notifications can be deleted with `DELETE /api/v1/alerts/{id}`. Active alerts and allocated prefix pools reject deletion. Both methods are included in the generated CLI commands and API types. Alert reads expose `kind` and the exact current monitoring `resource`; that resource is nullable and is not a historical resource record after resolution.

Privileged test controls remain outside the product API: VM creation and teardown, network/power/process failures, corrupt EFI and interrupted streams, retention-history and stopped-parent-dataset rollback fixtures, and independent storage/database/kernel observations. Local unpublished upgrade artifacts still require guest staging/start fixtures because production release-distribution and qualification checks remain enforced; the suite does not replace the installed hostd binary. Multi-node API calls use guest-local HTTP through the guest agent so deliberate NIC partitions do not disconnect the test controller. Intentionally retained workload data stays on its disposable VM until teardown; a general retained-dataset purge API requires durable ownership tracking before it can safely support reused dataset names.

Endpoint evidence distinguishes successful execution from safety rejections and outstanding multi-node requirements. Filtered runs do not claim complete endpoint coverage. Statement coverage is separate and includes FloatLab control and hostd processes, not third-party services. Coverage artifacts are enabled only for integration builds; release builds remain uninstrumented.

The suite preserves workload networking, persistence, snapshot and Compose recovery, upgrade rollback, independent SSE subscribers, alert ingestion, service restarts, lab enrollment and partition recovery, replication resume, backup restore, and installed UEFI upgrade checks. Run the full command twice consecutively when qualifying repeatability. Passing local upgrade fixtures does not qualify other published Docker/rqlite transitions or physical watchdog behavior.

Snapshot recovery is now `POST /api/v1/stacks/{id}/snapshots/{snapshotId}/restore`. `POST /api/v1/stacks/{id}/restore` performs failback to the primary. Clients of the old snapshot recovery path must migrate. Management requests require an administrator token, except login, public health routes, and the internal metrics webhook; deployments must restrict webhook ingress to trusted alert producers.

## CLI shell completion

The CLI and live command palette share modular definitions in [`api/opencli/`](api/opencli/README.md). Autocomplete scripts are bundled in the `lab` binary; no separate download is needed.

```sh
lab install-autocomplete bash
lab install-autocomplete fish
lab install-autocomplete ps1
lab install-autocomplete nu
```

Omit the shell to use `$SHELL` (or PowerShell when `PSModulePath` is set). Restart your shell after installing. The command works offline without a FloatLab session and can be run again after upgrading `lab`.

Bash uses `~/.bashrc`; Fish uses `$XDG_CONFIG_HOME/fish/completions/lab.fish` (default `~/.config/fish/completions/lab.fish`). PowerShell queries `pwsh` for its current-user, all-hosts profile, and Nushell queries `nu` for `config.nu`, so those shells must be on PATH. Installation updates only the marked FloatLab block and preserves the rest of each file. Nushell chains the previous external completer for other tools; external completion must be enabled.

`lab completion <bash|zsh|fish|powershell|nushell>` still prints a script for manual setup. Zsh users can run `source <(lab completion zsh)` after `compinit` in `~/.zshrc`.

Static commands, options, and choices work offline. Live stack/node/container suggestions use `FLOATLAB_URL`/`FLOATLAB_TOKEN` or the saved session. Completion never prompts for credentials or changes state; missing/expired credentials, authorization failures, and requests taking over one second simply produce no live suggestions. PowerShell completion works with the existing Linux/macOS binaries; no Windows binary is supplied.

The scripts call `lab completion resolve --shell <shell> --index <n> -- <words...>`. Words exclude the executable and include the current partial or empty word; the index is zero-based. Output is shell-specific, intended for the shipped adapter. No shell evaluates suggestion text as code. FloatLab completion ends at `exec … --`.

Maintainers: run `pnpm commands:generate` after changing modules, `pnpm commands:check` to validate them, and `pnpm commands:coverage` for the report-only OpenAPI mapping report in `dist/command-coverage/`. See the contract README for metadata and mapping rules.
