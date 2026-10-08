FloatLab Core
===

FloatLab Core is the brain behind running your FloatLab. It contains several core components that are used
to orchestrate your workloads.

## CLI

Download the matching `floatlab` release binary, mark it executable, and put it on your `PATH`:

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
