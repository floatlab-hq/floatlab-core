# Host OS and hostd changelog

## Unreleased

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
