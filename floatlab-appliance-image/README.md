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

## Fast test VM

```bash
nix develop
just run
```

This incrementally rebuilds the ISO, replaces any existing `floatlab-dev` VM, creates a thin 16 GiB qcow2 volume in libvirt's `default` pool, and starts the VM with KVM. The appliance recognizes that test disk, creates the `floatlab` ZFS pool, and uses the same bridged DHCP default as production on its first boot.

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

The ISO contains a template Compose file. It is installed only when `/floatlab/system/docker-compose.yml` is absent, allowing FloatLab to own upgrades after initial seed. The control-plane image is built from the same source and embedded in the ISO; third-party service images are pulled on first boot and must be pinned by digest before release.

## Next implementation work

1. Replace placeholder container tags with immutable digests.
2. Decide whether release images also embed the third-party service images for fully offline seeding.
3. Add an explicit onboarding target instead of allowing required service failures to leave the system at a console.
4. Generate and persist a unique `networking.hostId` per node rather than using the development placeholder.
5. Add NixOS VM tests for pool-present, pool-absent, and Compose-seed paths.
6. Add PXE outputs (`kernel`, `initrd`, and squashfs/root image) after the ISO path is stable.
