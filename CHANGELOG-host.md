# Host OS and hostd changelog

## Unreleased

## 0.2.0

- Bound update sandboxes to four 384 MiB services with no additional swap, explicit Go/cache budgets, and 512 MiB of extra host headroom; validate cloned-state recovery on 4 GiB appliances with measured cgroup peaks and OOM counters.
- Reserve memory for management and updates when setting the default ZFS ARC budget at boot; preserve explicitly configured ARC limits.
- Install the host daemon in the appliance command path so restricted SSH backup receivers can launch it outside systemd's service environment.
- Run appliance workload, management-update, installed host-upgrade, and multi-node acceptance with fixed 4 GiB VMs.
- Accept rqlite's leading v in reported versions when checking explicitly tested management update sources; continue rejecting unlisted versions.

## 0.1.5

- Preserve stack identifiers, operation identifiers, and severity from structured journal messages as searchable log fields so provisioning diagnostics appear in stack API log queries.
- Keep original journal messages and host identity intact; ignore malformed structured fields and avoid promoting unrelated payload fields.

## 0.1.4

- Add verified full and incremental ZFS backups over restricted, host-key-pinned lab SSH, preserving intermediate snapshots and resuming interrupted receives.
- Protect each destination's last verified recovery point with ZFS holds and `floatlab:` metadata; fence stale transfers and removal cleanup by configuration generation.
- Use RFD-0008 `fl-{short_timestamp}` automated snapshot names and overlapping time-based retention; clean removed destinations' holds and rerun source pruning.
- Restore backups through operation-owned staging, either by local clone or resumable remote transfer, with ownership and GUID checks before writable activation. Preserve backup history and reject existing destination datasets.
- Validate replication and both restore paths on three real ZFS appliances, including interrupted receive recovery, offline-source restoration, writable recovered data, and hold cleanup.

## 0.1.3

- Load the local root recovery password through native NixOS account activation from `/floatlab/system/etc/root-password.hash`, outside the immutable distro image.
- Mount persistent configuration datasets in the initrd so the recovery hash is available before account activation, without a Nix build at boot or a dependency on FloatLab, Docker, or rqlite.
- Require a separately entered and confirmed console recovery password in the installer. Persist only its hash in a root-owned, mode `0600` file; keep SSH root password login disabled.
- Verify fresh installation through the libvirt serial console, two disk-only cold boots, rejection of the FloatLab password for root, non-root hash access denial, and recovery login with Docker and rqlite stopped. Add a focused NixOS password and permissions regression test.
- Existing installations need the recovery hash provisioned separately. The updated installer fields ship with a new ISO built from this release's source; publishing the host tag alone does not publish an ISO.

## 0.1.2

- Add hostd-coordinated management upgrades with isolated recursive ZFS clone self-tests, restricted read-only IPC, durable recovery, and offline snapshot rollback.
- Stage verified OS releases on ZFS, boot one-shot UEFI trials with an independent readiness/heartbeat guard, and require explicit commitment before changing the permanent boot.
- Preserve workload datasets and Docker restart policies through recovery; clean only updater-owned obsolete datasets and downloads.
- Establish update recovery protocol 1. Older releases without this protocol require installing an update-capable baseline; Docker engine transitions remain disabled until individually qualified. Physical watchdog behavior has not been qualified.

## 0.1.1

- Publish a versioned host payload for the first offline installer release.

## 0.1.0

- Establish independent host releases and embedded version metadata.
- Report host versions through `sys.version`.
- Start the management stack from verified offline container bundles.
- Keep installed dataset setup and development file watchers free of boot ordering cycles.
