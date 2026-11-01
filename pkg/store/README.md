# pkg/store

ZFS management via `os/exec` — no CGo, no libzfs dependency (DEV-003).

## Dataset Layout

```
<pool>/                              # e.g. "floatlab"
├── floatlab-config/
│   └── node-<name>/                 # Per-node config (replicated to all online nodes)
├── floatlab-stats/                  # VictoriaMetrics data volume
├── floatlab-logs/                   # VictoriaLogs data volume
└── stacks/
    └── <stack-name>/                # One dataset per stack
        ├── <service-name>/
        │   ├── <bind-mount-name>    # Named bind mount
        │   └── vol-<volume-name>    # Named volume
        └── ...
```

## Snapshot Naming (normative)

| Kind | Pattern | Example |
|------|---------|---------|
| User (ad-hoc) | `user-YYYYMMDD-HHMMSS-<label>` | `user-20240101-120000-pre-deploy` |
| Automated workloads and backups | `fl-<timestamp>` | `fl-0J8F40` |

Automated timestamps use [RFD-0008](../../rfd/rfd-0008.md): six uppercase Crockford Base32 characters, three-second ticks, and a 2025 UTC epoch. A logical snapshot set shares one timestamp. Collisions require verified operation ownership or a new tick; existing snapshots are never overwritten.

Legacy snapshots remain readable and are excluded from the new automatic pruning policy. Snapshot purpose is metadata rather than a second naming hierarchy.

## Backup policies and protection

A managed Compose mount can declare local and destination policies:

```yaml
services:
  app:
    image: example/app
    volumes:
      - type: bind
        source: ./data
        target: /data
        x-fl-snapshots: h/24 d/28
        x-fl-backups:
          - node_id: destination-node-id
            schedule: h/24 d/28
```

The source takes one snapshot at each UTC boundary required by any local or backup tier. `1h/24 1d/28` is equivalent to `h/24 d/28`. Policies describe elapsed windows, not counts of successful runs: missed runs do not extend retention. Tier selections overlap, so a daily point survives expiry from the hourly window. Each backup prunes its own received history only after successful replication.

Source snapshots expose `floatlab:operation-id`, `floatlab:purpose`, `floatlab:hold-targets`, and `floatlab:transfer-targets`. Target properties contain JSON arrays of stable node IDs. Actual ZFS holds enforce the protection; the CLI and dashboard resolve IDs to node names. After a verified transfer, the node reference moves from its old recovery point to its new one. Other nodes' references remain intact.

The source dataset also stores `floatlab:target-generations` and `floatlab:removed-targets` as JSON maps of node IDs to configuration generations. These prevent stale transfers or delayed cleanup from changing a re-added target's protection. Changing an enabled target's source requires removal and acknowledged cleanup on the old source first.

Between runs, each destination protects its last verified source snapshot. Active/resumable transfers also protect required stream snapshots. Intermediate snapshots still available on the source are transferred; an offline destination can miss intermediate history already removed by source retention.

Dedicated backup datasets follow the source and are received read-only and unmounted. Conflicting destination-only changes can be rolled back automatically. Older retained backup snapshots survive source pruning. Missing common snapshot GUIDs stop replication and raise an incident rather than replacing retained history.

Recovery follows the token-first and GUID-verification approach reviewed in [modd-zfs-backup](https://github.com/moddengine/modd-zfs-backup/blob/main/main.go): validate the saved receive token against the source, resume with `zfs send -t`, verify the received GUID, and acquire the new recovery hold before releasing the old one. FloatLab additionally retires an unusable saved receive with `zfs receive -A` and retries from a verified common snapshot. It never automatically replaces retained history when no common snapshot exists.

On target or node removal, scheduling stops, pending source cleanup releases only that target's FloatLab holds and properties, and source retention runs again. Unreachable sources keep durable cleanup work until reconnection. Removing a target preserves received backup data.

Backup policies retain their primary-node ownership. Scheduling and manual replication pause while a workload runs on its failover node; automatic migration of the backup source and its recovery points is separate work.

## Restore a backup

The dashboard's backup panel and `backup restore <stack> <new-name> <backup-node> <destination-node> <snapshot> [--source node] [--file compose.yml]` restore into a separate stack and start it. Choose the backup node itself for a local clone, or another lab node for a resumable transfer. Every writable mount must have a selected received snapshot. Existing stack names and destination datasets are rejected; retained backup history is preserved.

`GET /api/v1/storage/backups?node_id=...&source_node_id=...&dataset=...` lists received points. `GET /api/v1/storage/backups/config?stack_id=...` reads the latest published configuration replica without requiring the source to be online. This configuration is not a historical snapshot manifest: review or supply the Compose configuration that matches the data, including a network pool available on the destination. Build contexts, environment files, file-backed configs/secrets and read-only project assets absent from the backup are rejected. The restored project has its own name, destination owner and generated container names, with the original failover and backup assignments removed.

`POST /api/v1/storage/backups/restore` accepts `name`, `source_node_id`, `backup_node_id`, `destination_node_id`, `compose_file` and `snapshots: [{dataset, snapshot}]`, with an `Idempotency-Key`. The destination owns the returned operation. All mounts are verified and activated before normal provisioning allocates networking and starts containers. Data transfer checkpoints survive worker restarts. Failed transfers preserve the source snapshot for retry; local clones keep their backup origin snapshot protected for the clone's lifetime.

## Key Functions

```go
// zfs.go
func DatasetCreate(ctx, name string, opts DatasetOptions) error
func DatasetDestroy(ctx, name string, recursive bool) error
func DatasetList(ctx, pool string) ([]Dataset, error)
func PoolHealth(ctx, pool string) (Pool, error)

// snapshot.go
func SnapshotCreate(ctx, dataset, name string, recursive bool) error
func SnapshotDestroy(ctx, dataset, name string) error
func SnapshotList(ctx, dataset string) ([]Snapshot, error)
func SnapshotRollback(ctx, dataset, name string, destroyNewer bool) error

// replication.go
func SendIncremental(ctx context.Context, opts SendOpts) error
func ReceiveStream(ctx context.Context, dataset string, r io.Reader) error
```

`SendOpts` includes source dataset, base snapshot (nil for initial seed), destination snapshot name, destination host:port, and a 1 MB I/O buffer size.

## ZFS Options

```go
type DatasetOptions struct {
    BlockSize   string // "4k"|"8k"|"32k"|"128k"
    Compression string // "none"|"lz4"|"gzip"|"zstd"
    Quota       string // e.g. "20G", empty = no quota
    Mountpoint  string // absolute path or "none"
}
```

## Fault Monitoring

`faults.go` polls `zpool status -j` every 60s and compares the vdev state tree against the last known state. On transition to DEGRADED/FAULTED, it calls `pkg/notify.Publish()` with severity=critical and records the fault in rqlite `zfs_faults` table.

## ZFS Exec Wrapper

All commands are run via `exec.CommandContext` with a 60s default timeout. Stdout/stderr are captured; non-zero exit codes are returned as structured errors with the full stderr body. The package never shells to user-controlled input — all parameters are passed as separate `exec.Cmd.Args` elements, not interpolated into shell strings.
