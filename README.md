FloatLab Core
===

FloatLab Core is the brain behind running your FloatLab. It contains several core components that are used
to orchestrate your workloads.

## CLI

Download the matching `floatlab` binary from a `manage-vX.Y.Z` release, mark it executable, and put it on your `PATH`:

```bash
chmod +x floatlab-linux-x64
mv floatlab-linux-x64 ~/.local/bin/floatlab
```

Set `FLOATLAB_URL` and `FLOATLAB_TOKEN` for scripts. Otherwise run `floatlab` in a terminal; it asks for the URL and credentials, then stores only the URL, token, and expiry in `~/.config/floatlab/config.json` (or `$XDG_CONFIG_HOME/floatlab/config.json`). Direct commands do not ask for lifecycle confirmation; interactive commands do.

```bash
floatlab list
floatlab logs plex --limit 100
floatlab                 # interactive prompt
```

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

Management updates first test an isolated clone, then stop production, take a
fresh rollback snapshot, and upgrade its existing datasets. Failure restores
the snapshot and previous services. Preflight currently requires 4.5 GiB of
available memory for four capped test services and host headroom. The API can disconnect during maintenance;
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

The repository's Nix development shell provides the VM tooling. Host libvirt must be installed, running, and accessible to your user.

Create and boot a fresh `floatlab-dev` VM:

```bash
nix develop -c env LIBVIRT_URI=qemu:///system scripts/create-floatlab-vm.sh
```

The command builds and loads the management API container, installs the native host daemon, and starts rqlite, VictoriaMetrics, VictoriaLogs, and the management API. The services start automatically after a VM reboot.

Once provisioning finishes, the API and Swagger UI are available on the VM's host-facing IP:

```text
http://<VM-IP>:8080
http://<VM-IP>:8080/swagger/
```

Start an existing VM:

```bash
nix develop -c virsh --connect qemu:///system start floatlab-dev
```

Find its IP address and connect:

```bash
nix develop -c virsh --connect qemu:///system domifaddr floatlab-dev --source lease
ssh ubuntu@<IP>
```

Create a VM and run the uncached container API integration suite:

```bash
nix develop -c env LIBVIRT_URI=qemu:///system RUN_INTEGRATION_TESTS=1 scripts/create-floatlab-vm.sh
```

The integration suite destroys the `floatlab-dev` domain and removes its generated files when it finishes, including after a failure.

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

Install the workspace dependencies with `pnpm install --frozen-lockfile`, then run:

```bash
nix develop -c env FLOATLAB_CLI_INTEGRATION=1 LIBVIRT_URI=qemu:///system \
  go test -run '^TestCLICaddy$' -count=1 -timeout 30m -v ./integration
```

The opt-in test requires Bun, Go, Nix with flakes enabled, libvirt, KVM, and access to pull Docker images. It builds the CLI and appliance, boots a dedicated VM, and tests Caddy HTTP responses, service DNS, managed IPv4 allocation and port binding, actual logs and metrics, command output and exit codes, and deletion cleanup. It cleans up the VM unless `FLOATLAB_KEEP_VM=1` is set. Set `VM_NAME` to choose the disposable VM name; that VM is recreated by the existing appliance launcher.

To test an already provisioned development VM with the updated services, add `API_URL=http://<VM-IP>:8080` and `VM_SSH=ubuntu@<VM-IP>`; SSH must support noninteractive authentication and passwordless sudo. This mode never creates or destroys the VM. The suite creates and removes only its own stack, pool, and bridge (if absent). Its test subnet is selected from unused `/24` ranges in `10.254.240.0/20`.

Hostd forwards metrics and logs for FloatLab containers every five seconds to the local VictoriaMetrics and VictoriaLogs endpoints. Set `FLOATLAB_NODE_ID`, `FLOATLAB_VMETRICS_URL`, and `FLOATLAB_VLOGS_URL` in its environment for another deployment. The core Compose files expose these endpoints only on loopback. Log polling currently captures at most 1000 lines and 1 MiB per container per pass; higher-volume workloads need continuous log forwarding.

### Complete single-node workload suite

Enter the development shell, install workspace dependencies, and run:

```bash
nix develop -c scripts/test-workloads.sh
```

This command requires Go, Bun, pnpm, Nix, libvirt, `virt-install`, KVM, and image-pull access. It performs two consecutive full passes, each using one uniquely named disposable appliance. All API and CLI scenarios within a pass share that appliance. The command creates a temporary libvirt storage pool unless `LIBVIRT_POOL` selects an existing writable pool. Reports and logs are written to `/tmp/floatlab-workload-reports`; override with `FLOATLAB_REPORT_DIR`. `FLOATLAB_KEEP_VM=1` preserves appliances for debugging.

An existing **dedicated test appliance** can be selected with `API_URL` and `VM_SSH`; SSH must be noninteractive with passwordless sudo. A local libvirt appliance can instead use `API_URL` and `VM_NAME` through its existing QEMU guest agent. The suite restarts `floatlab-control` and `floatlab-hostd`, requires the standard `floatlab-system` Compose project and `floatlab-hostd.service`, and removes only its fixture resources. It does not create or destroy that appliance. Do not point the suite at a shared deployment.

Coverage is measured by management HTTP method/path, using executed scenarios that pass their assertions, documented response statuses, and JSON contracts. The full suite fails if a required scenario skips or an endpoint lacks qualifying evidence. Real multi-node replication, failover, failback, abort and step-history success are reported as deferred; their single-node safety rejections are exercised. Ordinary `go test` skips VM tests and does **not** establish complete endpoint coverage.

The suite exercises actual workload networking, data persistence, snapshot recovery of data and Compose, failed health-check upgrade rollback, dataset rollback with newer snapshots, independent SSE subscribers, alert ingestion, notification state changes, and service restart recovery. Fast SQL integration and migration tests use Python 3's SQLite behind the rqlite HTTP protocol; appliance tests use real rqlite.

Snapshot recovery is now `POST /api/v1/stacks/{id}/snapshots/{snapshotId}/restore`. `POST /api/v1/stacks/{id}/restore` performs failback to the primary. Clients of the old snapshot recovery path must migrate. Management requests require an administrator token, except login, public health routes, and the internal metrics webhook; deployments must restrict webhook ingress to trusted alert producers.
