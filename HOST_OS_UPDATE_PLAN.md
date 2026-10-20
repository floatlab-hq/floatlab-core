# Host and OS Self-Update Plan

Status: implementation added; real appliance acceptance and Docker transition qualification remain unverified. Updated 2026-10-20.

## Summary and decisions

Download and verify a host release into a new child dataset of the `floatlab` pool, stage it as `next`, explicitly reboot into it once, and explicitly commit it as the permanent boot after health checks. A healthy uncommitted trial may keep running indefinitely; the next reboot returns to the previously committed OS and its management recovery point.

Keep one previous committed OS and management rollback version. Clean up older updater-owned versions only after successful commitment and reference checks. Updates are local to a single appliance; cluster sequencing, workload migration, and automatic maintenance windows are deferred.

Management-stack preflight, production startup, and snapshot restoration are defined in [MANAGEMENT_UPDATE_PLAN.md](MANAGEMENT_UPDATE_PLAN.md). The local hostd coordinates both flows; it must recover an OS trial without depending on a healthy management container or rqlite.

## OS storage and boot selection

Extend the existing installed-image layout:

| Dataset | Purpose |
| --- | --- |
| `floatlab/os/<version>@base` | Verified pristine OS release, including its kernel and initramfs. |
| `floatlab/os/current` | Read-only boot clone for the committed release. |
| `floatlab/os/next` | Read-only boot clone for the staged trial release. |
| `floatlab/os/previous` | Retained boot clone for the previous committed release. |
| `floatlab/os/live` | Writable disposable root recreated for the selected OS on every boot. |

Keep version datasets and `live` hidden from ordinary boot selection. Preserve the existing prohibition against destroying a mounted `live` dataset or recursively removing unexpected snapshots and dependent clones.

Change the initrd root setup to derive the pristine snapshot from the boot environment actually selected by the bootloader, rather than always reading `current`. Validate the selected environment, origin, host identity, manifest, and read-only properties before recreating `live`. Preserve strict pool import and the existing OpenZFS compatibility policy.

Use UEFI `BootNext` for the one-shot trial, pointing at a separate ZFSBootMenu EFI entry configured to prefer `floatlab/os/next`. The ordinary boot entry and pool `bootfs` continue selecting `current`; do not make `next` the persistent default merely to try it. ZFSBootMenu supports a preferred dataset through its [`zbm.prefer` parameter](https://docs.zfsbootmenu.org/en/v3.1.x/man/zfsbootmenu.7.html#command-line-parameters).

Provision and verify the trial EFI file and entry without overwriting the known-good fallback EFI executable. Support the installer's multiple ESPs, validate the selected trial entry, and verify firmware consumes `BootNext` before trial startup. If one-shot selection cannot be established, permit staging but reject activation with an actionable error. Test failure before the candidate kernel and initrd start; consuming a flag only in the new OS is insufficient.

Ordinary OS updates retain the tested bootloader and pool feature compatibility. A release requiring a new bootloader or pool feature must be rejected until a separate recoverable bootloader transition has been implemented and tested.

## Staging, trial, commitment, and rollback

1. **Resolve and validate.** Select an explicit official `host-v` release and, when required, a compatible management release bundle. Authenticate the official source through HTTPS and verify manifest fields, archive hashes, architecture, version, init path, bootloader compatibility, and the Docker/shared-state compatibility gate below. Treat compatibility as an activation gate, even though existing runtime compatibility reporting is advisory.
2. **Stage the OS.** Check pool health and free space. Download to an operation-owned partial file, verify it before extraction, and reuse the installer's archive validation, Nix registration, manifest, host-ID, and boot-property setup. Populate `floatlab/os/<version>`, create `@base`, make it read-only, and create/configure `next`. Do not call installer code that overwrites `current` or `bootfs`. Share safe image-population logic rather than duplicating it. Existing matching staged content is reusable; mismatched or foreign content is rejected.
3. **Prepare management.** Run the management clone preflight for the selected service bundle. Hostd records the old and target OS/management pairing outside the management rollback tree. A host-only update still takes a management recovery point because the trial hostd or management startup can change persistent management state. A preflight on the old host does not establish compatibility with the new kernel; the real boot trial supplies that check.
4. **Activate explicitly.** On administrator request, acquire the shared update lock, verify the independent boot-recovery guard is installed and enabled in both OS releases, quiesce management, persist management-container restart suppression, stop its stateful services, and take the final clean recursive management rollback snapshot. Persist the boot transaction and previous image/Compose definitions, install and verify the one-shot boot entry, then reboot cleanly. Workload downtime from reboot is expected on a single appliance. If activation fails before reboot, clear any one-shot selection, restore and verify the prior management services, then restore their normal restart policies and report failure.
5. **Start the trial.** Arm the independent recovery deadline in early userspace before networking, Docker, or hostd startup. Before core-stack startup, identify the running OS from its actual image manifest and reconcile it with the boot journal. Initialize networking and start hostd, then start the selected management services through the reconciliation gate and restart-policy override defined in the management plan. Verify root origin, host identity, ZFS import, required mounts, network availability, hostd IPC, Docker, management readiness, rqlite, application Raft, and observability. Require readiness within ten minutes of the guard starting and 60 seconds of stable health before enabling commitment. Starting hostd alone does not satisfy or cancel the recovery deadline.
6. **Remain uncommitted.** A healthy trial stays running until the administrator commits or reboots. Permit normal management operations after readiness, as requested. Keep the old OS, management images, and final pre-trial management snapshot protected. Block a second update while this trial is pending. A normal reboot without commitment boots `current` and restores the pre-trial management state before its services start.
7. **Commit explicitly.** Require the running trial to match the staged release and pass health checks. Journal the old/new boot clones and each promotion step. Preserve the old committed boot clone as `previous`, promote the trial clone to `current`, and make `bootfs` select it. Keep a valid known-good boot target throughout interrupted rename/property updates; reconcile the actual datasets and journal before accepting another boot transaction. Finish the associated management commitment and then permit retention cleanup.
8. **Recover a failed trial.** Stop management if it started, preserve the failure diagnosis outside the rollback tree, clear one-shot selection, and reboot to the committed OS. Its hostd restores the complete final pre-trial management snapshot and old image/Compose configuration before normal core-stack startup, then verifies the recovered stack. Never start the old rqlite image against state modified by the trial. If restoration fails, report recovery failure and keep management stopped rather than mixing versions.

The previous and trial hostd must understand the same transaction journal and rollback procedure. Release validation checks this recovery-protocol compatibility before activation. The first supported baseline must ship this boot/recovery support; older images need a documented bootstrap installation before automated trials are enabled. Do not advertise rollback to a baseline unable to interpret the recovery journal.

## Docker and shared host-state compatibility

OS rollback restores OS selection and management snapshots, not Docker's shared `/floatlab/system/docker` data root or workload data. A trial daemon can modify engine metadata before management becomes ready. Management preflight runs under the current host's Docker and cannot validate a new host daemon's ability to upgrade or downgrade this shared state.

Extend host release metadata with the packaged Docker/containerd versions, ZFS storage-driver configuration, and an explicit list of source host releases for which forward upgrade and fallback have both passed qualification. Hostd validates the actual running engine/storage configuration against the source release and checks the target's supported transition before arming `BootNext`. Missing metadata, unknown runtime drift, a storage-driver/store-layout change, or an unqualified transition blocks activation; staging may still complete. A matching major version alone is not evidence of reversibility.

For each advertised source/target pair, release CI must boot the source OS with existing management and workload containers, images, volumes, and networks; boot the target against that same persistent Docker data root; exercise container and network operations; then fall back to the source OS without restoring the Docker data root. Verify the old daemon can recover its metadata, start management and workloads, preserve workload data, and continue normal Docker operations. Exercise both failed trial startup and a healthy uncommitted trial that later reboots. Include changes made by the trial daemon, not just an idle daemon restart.

Reject Docker/OS transitions that cannot pass this reverse-path test. Handling incompatible engine state requires a separate coordinated workload maintenance/migration design; do not add a whole-Docker-root rollback that would discard unrelated workload changes. Existing pool-feature and bootloader gates remain independently required.

## Failure handling and retention

- Journal download, staging, management checkpoint, boot arming, trial startup, promotion, restoration, and cleanup independently. Reconcile interrupted operations from durable facts; never repeatedly reboot into a failed candidate.
- Ship a small independent systemd boot-recovery guard in the supported baseline and every target image. On a trial boot, start it immediately after the root is available, before management-state mounts, network initialization, Docker, and hostd. Identify the trial from an operation token in the trial boot environment's kernel command line; arming the ten-minute monotonic startup deadline must not wait for those services or rqlite. The guard has no `Requires=` dependency on them and runs independently of hostd's process and IPC socket.
- Hostd remains the upgrade coordinator. It claims supervision using the matching operation ID and reports readiness and subsequent health to the guard. Only verified stable trial readiness transitions the guard from the startup deadline to heartbeat monitoring; hostd merely starting does not cancel protection. During a healthy uncommitted trial, hostd sends a heartbeat every ten seconds; sixty seconds without one triggers fallback, allowing bounded hostd restarts without leaving recovery unowned. Once durable commitment is confirmed, retire the guard and clear the trial token from the committed boot environment. Readiness/heartbeat messages from another operation cannot disarm it.
- If the startup deadline expires, a prerequisite fails, or supervision is lost, the guard records a failure when persistent storage is available and requests a bounded reboot independently of hostd and network services. It does not restore mounted databases: the committed OS performs management restoration behind the startup gate after reboot. `BootNext` has already been consumed. A fallback boot uses the ordinary entry without the trial token, so recovery failure there leaves management stopped and reports the error instead of causing a trial reboot loop.
- Configure a usable hardware watchdog where available and validate it through the boot path, including a stalled shutdown. The independent guard covers failed userspace startup while PID1 is healthy; neither it nor a systemd timer can recover a hard kernel hang. A hardware watchdog fed by healthy PID1 alone does not replace the trial deadline. See [systemd watchdog settings](https://www.freedesktop.org/software/systemd/man/257/systemd-system.conf.html).
- Permit updates on hardware without a usable watchdog, but expose that a kernel/early-boot hang can require a physical reset. After reset, the consumed one-shot selection must lead to the committed OS.
- Preserve the running OS's origin even after commitment; `live` can still depend on it until the next reboot. Never delete datasets referenced by current/previous/next/live, a pending journal, retained management recovery points, or unexpected external clones/holds.
- Keep current plus one previous committed version. After commitment, enumerate references and remove only older updater-owned boot clones, version snapshots/datasets, downloaded archives, and unneeded management images. Use explicit dependency-aware deletions; never broad recursive destruction or global Docker pruning. Cleanup failure is reported and retryable without undoing a successful commitment.
- Retain recovery logs outside the management rollback tree. Rollback intentionally discards post-checkpoint management configuration and telemetry, and does not undo external workload changes made during the trial.

## Interfaces and operator workflow

Extend the same hostd transaction/status interface used for management updates with host staging, trial reboot, explicit commit, rollback, and cleanup. Expose administrator-authenticated management endpoints, generated client support, shared CLI commands, and a basic UI showing installed/staged/running/committed versions, health checks, progress, reboot requirements, watchdog capability, and rollback consequences.

Long-running requests return an operation ID; hostd continues after API disconnect or process restart. Preserve idempotency and actor attribution. A root host command provides status and recovery through hostd IPC while management is unavailable. The read-only preflight socket must not expose boot/update mutations.

Use selected immutable release versions and hashes/digests; no mutable `latest` dependency. Signed manifests, unattended activation, automatic commitment, new bootloader delivery, and cluster coordination are outside this first implementation.

## Acceptance tests and implementation order

Implement the management coordinator and its restore tests first, then the baseline initrd/firmware boot support, host image staging, trial recovery, promotion/cleanup, and operator interfaces. Existing installer image tests should exercise shared image-population logic after extraction from installer orchestration.

Release acceptance requires appliance VM tests with real ZFS, Docker, and UEFI firmware:

- Stage a verified new image while the old OS remains bootable; reject bad hashes, unsafe archives, wrong architecture, incompatible host/manage pairs, and insufficient space.
- Boot `next` once, recreate `live` from its exact pristine origin, and verify that a healthy uncommitted trial returns to `current` on reboot.
- Explicitly commit and verify subsequent boots use the new `current`, while `previous` and its management rollback point remain recoverable.
- Fail before kernel startup, during initrd/root setup, networking, hostd startup, database migration, and management readiness. Verify the next successful fallback boot restores the old stack's state and service versions before serving requests.
- Fail network initialization so hostd never starts; separately crash-loop hostd and fail Docker startup while PID1 stays healthy. Verify the independent guard triggers fallback without operator action. Kill hostd after healthy trial readiness and verify missed heartbeats trigger fallback; a healthy uncommitted trial must continue beyond ten minutes without rebooting. Wrong-operation readiness/heartbeats must not disarm recovery.
- Restart Docker and interrupt power during an uncommitted trial; verify management-container restart suppression survives and fallback restores state before management starts. Run every advertised Docker/source-host transition against the same persistent engine data root and real workload fixtures, and reject unqualified or drifted configurations before activation.
- Interrupt every promotion and restoration checkpoint, including dataset renames and boot-property updates. Verify a valid boot target, resumable recovery, and no candidate reboot loop.
- Exercise all installed ESPs, missing trial files, rejected/unwritable firmware variables, consumed one-shot entries, watchdog resets where supported, and a manual reset without a watchdog.
- Verify preflight network isolation and read-only IPC remain enforced during combined management/OS updates.
- Retain mounted origins, clones, holds, previous recovery points, and workload images through cleanup. Demonstrate old unreferenced versions are reclaimed after commitment.

VM tests establish the supported firmware and virtual-hardware path. Physical watchdog behavior and firmware quirks require hardware qualification before claiming automatic recovery on a particular appliance.
