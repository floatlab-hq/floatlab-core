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
3. The persistent networkd directory is bind-mounted at `/run/systemd/network`.
4. `floatlab-network-config.service` validates and reloads the persistent bridge/static-IP configuration.
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

This incrementally rebuilds the ISO, replaces any existing `floatlab-dev` VM, creates a thin 16 GiB qcow2 volume in libvirt's `default` pool, and starts the VM with KVM. The appliance recognizes that test disk, creates the `floatlab` ZFS pool, and seeds DHCP networking on its first boot.

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

## Manual network provisioning

For non-test disks, create the pool with `floatlab-dev-provision`, then add persistent network units under `/floatlab/system/etc/systemd/network`. A minimal bridge example follows.

`10-br0.netdev`:

```ini
[NetDev]
Name=br0
Kind=bridge
```

`20-uplink.network`:

```ini
[Match]
Name=enp1s0

[Network]
Bridge=br0
```

`30-br0.network`:

```ini
[Match]
Name=br0

[Network]
Address=192.168.1.50/24
Gateway=192.168.1.1
DNS=192.168.1.1
```

Then reboot so the complete normal boot path is exercised.

## Important implementation choices

### Ephemeral root

Do not define a normal persistent `/` filesystem. The imported NixOS ISO module already boots a compressed squashfs closure with an ephemeral writable layer. `/run`, `/tmp`, journald storage, and ordinary mutable state disappear at reboot.

### Persistent network configuration

Do not replace NixOS-managed `/etc`. Persistent network files are exposed through `/run/systemd/network`, which has higher runtime priority and is intended for generated configuration.

### Docker on ZFS

This scaffold sets Docker's `data-root` to `/floatlab/system/docker` and explicitly chooses the classic `zfs` storage driver. Validate this choice against the exact Docker version selected by Nixpkgs before production deployment. Newer Docker releases may default to the containerd image store; pinning behavior is safer than relying on automatic selection.

### Compose seeding

The ISO contains a template Compose file. It is installed only when `/floatlab/system/docker-compose.yml` is absent, allowing FloatLab to own upgrades after initial seed. The control-plane image is built from the same source and embedded in the ISO; third-party service images are pulled on first boot and must be pinned by digest before release.

## Next implementation work

1. Replace placeholder container tags with immutable digests.
2. Decide whether release images also embed the third-party service images for fully offline seeding.
3. Add an explicit onboarding target instead of allowing required service failures to leave the system at a console.
4. Generate and persist a unique `networking.hostId` per node rather than using the development placeholder.
5. Add NixOS VM tests for pool-present, pool-absent, missing-network-config, and Compose-seed paths.
6. Add PXE outputs (`kernel`, `initrd`, and squashfs/root image) after the ISO path is stable.
