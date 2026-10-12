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
nix build .#iso --print-build-logs
```

The ISO is produced under:

```text
result/iso/floatlab-appliance-*.iso
```

Equivalent helper:

```bash
./scripts/build-iso.sh
```

## Console installation

The production ISO opens the installer on the appliance screen and serial console.
Press Enter on either console to claim the single installation session.

1. Select disks, inspect their current partition tables and proposed layout, edit
   supported ZFS properties if needed, and type `ERASE ALL DATA` to confirm.
   One disk creates a single-disk pool; multiple disks form one mirror. Every disk
   receives an EFI partition, an immutable image partition, and a ZFS partition.
2. Enter the timezone, node name, and DHCP or static IPv4 settings while the
   systemd worker prepares storage and installs the image in the background.
3. Create the FloatLab administrator. When NTP is synchronized, optionally enroll
   an authenticator and save the ten single-use recovery codes. These credentials
   belong to FloatLab; they do not create a Linux account.
4. Wait for container images, service health, node registration, and administrator
   login to be verified. Success starts a 30-second reboot countdown. Press Enter
   to cancel it, then type `REBOOT` when ready.

Installation requires x86-64 UEFI with Secure Boot disabled and Internet access
for initial container image downloads. Each disk needs the image reservation
(at least 8 GiB), 512 MiB EFI space, and at least 16 GiB for ZFS. Installed boots
use cached container images and do not require registry access.

The worker survives a console disconnect; reopen the console to continue. Logs
are available through `journalctl -u floatlab-install-worker`. Disk identity or
partition changes invalidate erase confirmation. Destructive operations are never
replayed automatically. Once the pool checkpoint is saved, image copying and
finalization can be retried without formatting again. Resume incomplete image
copying from the original ISO, rather than the installed image being copied.
If interruption occurred before that checkpoint, inspect the partial disks/pool
before beginning a fresh, explicitly confirmed installation.

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
