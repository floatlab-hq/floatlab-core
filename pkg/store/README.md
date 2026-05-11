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
| Scheduled | `flsnap-YYYYMMDD-HHMMSS-<type>` | `flsnap-20240101-000000-daily` |
| Replication | `fsrepl-<seq>-YYYYMMDD-HHMMSS` | `fsrepl-0042-20240101-120000` |

Replication snapshots are named with a monotonically increasing sequence number. Once a job completes, the previous `fsrepl-*` snapshot on the destination is destroyed.

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
