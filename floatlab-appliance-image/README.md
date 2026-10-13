# FloatLab immutable appliance image

This repository is a concrete starting point for an immutable, squashfs-backed NixOS appliance that boots from ISO/USB and can later expose the same kernel/initrd for PXE. Runtime writes remain ephemeral unless explicitly directed to the ZFS pool named `floatlab`.

## Boot design

The standard NixOS live ISO supplies the Linux kernel, initrd, systemd, compressed squashfs system image, and ephemeral writable root. FloatLab adds an ordered systemd pipeline:

1. `floatlab-zfs-import.service` discovers and imports `floatlab` without forcing an import.
2. `floatlab-datasets.service` creates and mounts:
   - `floatlab/system` at `/floatlab/system`
   - `floatlab/system/etc`
   - `floatlab/system/docker`
   - `floatlab/system/db`
   - `floatlab/system/metrics`
   - `floatlab/system/logs`
3. `floatlab-network-config.service` runs `floatlab-hostd network init` after dataset mounting.
4. Hostd reconciles bridge/bond topology and static settings from `/floatlab/system/etc/network.toml`; systemd-networkd handles only DHCP on `floatlab-lan`.
5. Docker starts with `/floatlab/system/docker` as its persistent data root.
6. The bundled `docker-compose.yml` is copied only when the persistent copy is absent, and the image's locally built control-plane image is loaded into Docker.
7. `floatlab-hostd.service` starts the native host daemon and creates its Unix socket.
8. `floatlab-core-stack.service` starts rqlite, VictoriaMetrics, VictoriaLogs, and the control plane with `docker compose up -d`.
9. journald forwards host, kernel (`dmesg`), and Docker logs through rsyslog to VictoriaLogs' loopback-only syslog listener.

The import service deliberately does **not** use `zpool import -f`. A pool that appears active elsewhere should fail into maintenance, not risk simultaneous writers.

## Requirements

Build host:

- Linux x86-64
- Nix with flakes enabled
- Approximately 10–20 GB free for the Nix store and ISO build
- libvirt/KVM and `virt-install` for the fast test harness

Enable flakes in `~/.config/nix/nix.conf` or `/etc/nix/nix.conf`:

```ini
experimental-features = nix-command flakes
```

## Build the ISO

```bash
./scripts/build-iso.sh
```

The ISO is produced under:

```text
result/iso/floatlab-installer-{version}.iso
```

`version` is the Git tag pointing at HEAD, or the short Git commit hash when HEAD
is untagged. Tags must be present locally (fetch tags first in shallow CI checkouts).
Uncommitted changes retain HEAD's version. Set `FLOATLAB_INSTALLER_VERSION` to
override the version explicitly; the release workflow uses the triggering tag.

Direct Nix builds use the short commit hash; use the helper above for tag naming:

```bash
nix build .#iso --print-build-logs
```

## Tagged releases

Branch and pull-request CI runs lint, typechecks, contract checks and tests.
Production builds run only when a version tag is pushed, such as `v1.0.0`,
`v0.1.0-alpha.0` or `v0.1.0-rc`. The release workflow runs the same CI checks
before building the UI, Storybook, CLI binaries and production ISO. It checks
the installer's boot configuration and publishes the four Linux/macOS CLI
binaries, `floatlab-installer-{tag}.iso` and `SHA256SUMS` to a GitHub release.
Tags with a prerelease suffix create a GitHub prerelease.

The first planned test tag is `v0.1.0-alpha.0`. Once the release changes are
committed to the release commit, create and push that tag:

```bash
git tag v0.1.0-alpha.0
git push origin v0.1.0-alpha.0
```

Checks and builds run on GitHub-hosted Ubuntu runners, with Nix installed by
the release workflow. The first ISO build can take
longer while dependencies are downloaded and compiled. These checks do not run
the opt-in appliance installation VM suites. ZFS stream releases are deferred
until the ISO release path is validated.

## Console installation

The production ISO runs the Bubble Tea `install-tui` on the appliance screen and
serial console. Press Enter on either console to claim the single installation
session. The TUI launches `install-service` and exchanges newline-delimited JSON
through stdin/stdout; service diagnostics go to a private temporary log.

1. Select disks with checkboxes. Rows show the disk name, manufacturer, model,
   capacity, and existing partitions. One disk generates a single-disk pool command;
   multiple disks generate a mirror. Edit the full command for raidz or other vdev
   layouts. The bordered multiline editor groups the pool name, options, and
   topology into three lines. Enter inserts a newline; Ctrl+S validates and previews.
   The service parses arguments without a shell and dry-runs against whole
   disks. Inspect the rewritten command and partition plan, then type
   `ERASE ALL DATA` to confirm.
2. Installation starts immediately while you enter host and network settings.
   Choose a timezone from the searchable list and select DHCP or Static with the
   radio buttons. Static addressing fields appear only for Static. The service
   validates these settings before opening the separate administrator account step.
   Each disk receives a 512 MiB UEFI partition, an immutable Floatlab
   boot-image partition (at least 8 GiB), and a ZFS partition (at least 16 GiB).
   The service repeats the dry-run against the actual ZFS partitions before creating
   the pool. It copies and verifies the image and installs each UEFI bootloader.
3. Review and explicitly submit configuration. The service validates it immediately;
   errors remain editable. Optionally enroll TOTP when NTP is synchronized and save
   the ten single-use recovery codes. If copying finishes first, installation waits
   for valid configuration before applying settings and verifying the stack.
4. Success shows a 30-second reboot countdown. Press P or Space to pause it, or R
   or Enter to reboot immediately. Failure remains visible without a countdown;
   reboot manually after inspecting the error. There is no quit action or in-place
   installation retry.

The header shows branding and the input step. The bottom six lines stay dedicated
to installation status, activity, overall progress, copied bytes, and transfer speed.
Copying accounts for most progress; other operations indicate activity and milestones.

Installation requires x86-64 UEFI with Secure Boot disabled and Internet access for
initial container downloads. Installed boots use cached container images. On an
unexpected frontend disconnect, the service finishes the active operation, cleans
up, and stops before starting the next installation stage. Inspect partial disks
before reinstalling; destructive work is never replayed automatically.

### Try the TUI without installing

Run these commands from the repository root with Go installed:

```bash
go build -o /tmp/floatlab-install-service-mock ./cmd/install-service-mock
go run ./cmd/install-tui --mock --service /tmp/floatlab-install-service-mock
```

See the [installer developer guide](DEVELOPMENT.md) for UI controls, mock scenarios,
and development checks. To build standalone executables together:

```bash
go build -o /tmp/floatlab-installer/ ./cmd/install-tui ./cmd/install-service-mock
/tmp/floatlab-installer/install-tui --mock
/tmp/floatlab-installer/install-tui --mock --scenario rejected-config --step-delay 100ms
```

Mock scenarios: `success`, `delayed-config`, `rejected-config`, `failure`, and
`cancellation`. All use the real session protocol and simulated disks. Pool dry-runs
always validate; configuration validation and TOTP enrollment still work. Reboot
exits the mock instead of rebooting the host. `--service` selects a service executable.

The real and mock service protocols and lifecycle checks are covered by Go tests:

```bash
go test -race ./internal/installer ./internal/installtui
```

Settings, identity, credential encryption key, and checkpoints persist under
`/floatlab/system/etc`. Administrator bootstrap files contain a password hash and
encrypted TOTP secret; startup imports them without resetting existing accounts
and removes the credential file. TOTP login requires `totp_code` or a single-use
`recovery_code` at `/api/v1/auth/token`; the CLI prompts for the second factor.
Existing installations bypass the wizard and retain their persistent Compose file.

Run the real UEFI console and disk-only boot tests (including each mirror member
alone) outside the Nix build sandbox, where container downloads are available:

```console
nix run .#installer-test -- --iso /absolute/path/to/floatlab.iso
```

Use `--disks 1` or `--disks 2` for one layout and `--keep` to retain disposable
qcow2 disks and serial logs. The driver uses KVM when available and TCG otherwise.

Run the extended pre-merge checks against the same production ISO:

```console
nix run .#installer-test -- --iso /absolute/path/to/floatlab.iso --disks 1 --before-storage
nix run .#installer-test -- --iso /absolute/path/to/floatlab.iso --disks 1 --failures --static --timezone Australia/Melbourne
nix run .#installer-test -- --iso /absolute/path/to/floatlab.iso --disks 2 --totp --timezone Australia/Melbourne --cli /absolute/path/to/floatlab
```

`--before-storage` kills the worker at its first wipe-command boundary, then
restarts the VM and checks that disks remain unchanged. `--failures` disconnects
the console, reboots after the storage checkpoint, disables NTP, injects a failed
registry pull, retries finalization, and verifies repeated bootstrap and bypass
of the wizard for an existing Compose installation. `--static` uses `10.0.2.20`
on QEMU's isolated user network. `--totp` waits for NTP synchronization, enrolls
an authenticator, checks replay rejection and recovery-code persistence across
all mirror boots, and verifies that repeated bootstrap cannot reset credentials
or restore a consumed recovery code. Supply `--cli` with the compiled CLI to
exercise interactive login against the installed appliance. Run NTP-failure and
TOTP-enrollment cases separately. These tests modify only disposable VM disks.

Production ISO validation completed on 2026-10-12 on x86-64 Linux with KVM.
The three extended scenarios passed, including disk-only single-disk boot,
both mirror members together and each member alone, unchanged node UUIDs,
persisted hostnames and Melbourne timezone, static IPv4, management/service
health, administrator and interactive CLI login, TOTP replay rejection,
recovery-code consumption across reboots, repeated bootstrap without credential
reset, existing-installation bypass, console reconnection, interruptions before
and after the storage checkpoint, unavailable NTP, failed registry pulls and
successful retry, and cancellation of the automatic reboot countdown.
The ISO build and effective boot-configuration check passed, as did the existing
host-network VM regression, 129 installer/control/credential Go tests, and six
CLI tests. Physical hardware validation remains separate from these VM checks.

## Fast test VM

```bash
nix develop
just run
```

This explicitly builds the `dev-iso` variant, replaces any existing `floatlab-dev` VM, creates a thin 16 GiB qcow2 volume in libvirt's `default` pool, and starts the VM with KVM. The appliance recognizes that test disk, creates the `floatlab` ZFS pool, and uses the same bridged DHCP default as production on its first boot.

Open its serial console with:

```console
virsh -c qemu:///system console floatlab-dev
```

Override defaults when needed:

```bash
VM_NAME=test2 ZFS_DISK_SIZE=32G MEMORY_MB=8192 ./scripts/run-libvirt.sh
```

Run the container API integration test against a freshly built appliance VM:

```bash
FLOATLAB_VM_INTEGRATION=1 go test -count=1 -timeout 30m -v ./integration
```

`./scripts/run-qemu.sh` remains available as `just run-qemu`; its disk is persistent and still uses the manual provisioning flow below.

## Host network settings

Hostd reads `/floatlab/system/etc/network.toml` at startup. A fresh installation bridges physical Ethernet adapters and obtains an IPv4 DHCP lease. DNS defaults to `1.1.1.1` and `8.8.8.8`; an empty DNS list enables DHCP-provided DNS.

```toml
version = 1
excluded_macs = []
bonds = []
dns_servers = ["1.1.1.1", "8.8.8.8"]

[ipv4]
mode = "dhcp"
```

For static addressing, use `mode = "static"` with `address`, `netmask`, and `gateway`. LACP bonds use `[[bonds]]` with `name`, `mode = "802.3ad"`, and `members` containing permanent adapter MAC addresses. The switch must have matching LACP configuration. The bridge retains its original first-adapter MAC across topology changes and uses kernel STP to prevent loops between bridged uplinks.

The management API is `/api/v1/nodes/{id}/settings/network`. GET returns the confirmed configuration, revision, effective state, and adapter inventory. PUT accepts `{ "revision": "...", "config": {...} }`, requires an administrator bearer token and `Idempotency-Key`, and returns a change ID. Poll `/changes/{change}`, then POST `/changes/{change}/confirm` before the reported deadline. POST `/changes/{change}/rollback` restores immediately. A separate systemd timer restores unconfirmed changes after 60 seconds even when hostd or the API is unavailable. Confirmation and rollback do not depend on cluster-database writes.

The host-local journal lives beside TOML in `network.toml.state/`. Invalid TOML falls back to confirmed settings when available; otherwise startup fails. Missing TOML selects bridged DHCP and archives legacy networkd files. Generated DHCP configuration lives in `/run/systemd/network`; hostd owns bridge/bond creation, static addresses, and service aliases through netlink. Udev link metadata preserves hostd ownership markers and the chosen bridge MAC during device creation.

Service pools remain in the cluster database. Set explicit `node_ids` when creating a pool; select each host's eligible default using `default_pool_id` in its network settings. A pool with one member is host-bound; several members explicitly assert a shared LAN. Allocation and takeover also check the host's primary bridge subnet. Subnet matching does not automatically enroll new members. Unresolved legacy pool membership blocks new allocation and failover until corrected through the pool API.

Service aliases are restored from cluster allocation ownership after reboot, preventing a host from blindly reclaiming an address transferred to another node. The control plane retries local ownership reconciliation after DHCP startup and daemon reconnection.

Run the isolated DHCP/static, DNS, LACP, service-address, reboot, and rollback test with:

```console
nix build .#checks.x86_64-linux.host-network --print-build-logs
```

If the Nix build users cannot access `/dev/kvm`, build the driver and run it as a host user in the `kvm` group:

```console
nix build .#checks.x86_64-linux.host-network.driver -o result-network-driver
mkdir -p /tmp/floatlab-network-test
./result-network-driver/bin/nixos-test-driver --no-interactive -o /tmp/floatlab-network-test
```

## Important implementation choices

### Ephemeral root

Do not define a normal persistent `/` filesystem. The imported NixOS ISO module already boots a compressed squashfs closure with an ephemeral writable layer. `/run`, `/tmp`, journald storage, and ordinary mutable state disappear at reboot.

### Persistent network configuration

Do not replace NixOS-managed `/etc`. Persistent TOML and the recovery journal live on the FloatLab volume. Only generated bridge DHCP configuration is written to `/run/systemd/network`; physical adapters and bonds are not managed by networkd.

### Docker on ZFS

This scaffold sets Docker's `data-root` to `/floatlab/system/docker` and explicitly chooses the classic `zfs` storage driver. Validate this choice against the exact Docker version selected by Nixpkgs before production deployment. Newer Docker releases may default to the containerd image store; pinning behavior is safer than relying on automatic selection.

### Compose seeding

The ISO contains a template Compose file. It is installed only when `/floatlab/system/docker-compose.yml` is absent, allowing FloatLab to own upgrades after initial seed. The control-plane image is built from the same source and embedded in the ISO; third-party service images are fetched during installation finalization and must be pinned by digest before release.

## Next implementation work

1. Replace placeholder container tags with immutable digests.
2. Decide whether release images also embed the third-party service images for fully offline seeding.
3. Add NixOS VM tests for pool-present, pool-absent, and Compose-seed paths.
4. Add PXE outputs (`kernel`, `initrd`, and squashfs/root image) after the ISO path is stable.
