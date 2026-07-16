# FloatLab ZFS Host Abstraction Plan

## Goal

Implement the privileged ZFS boundary as two parts:

1. The FloatLab management container owns API orchestration and relays snapshot streams between nodes.
2. The bare-metal `floatlab-agent` executes ZFS commands, reads Linux/ZFS counters, and pushes metrics to VictoriaMetrics.

Extend the existing Unix-socket HTTP agent. Do not add gRPC, a metrics SDK, temporary snapshot files, a persistent metrics queue, or an asynchronous job framework.

## Existing Code To Extend

- `internal/agent/zfs.go`: current command-backed ZFS implementation.
- `internal/agent/service.go`: current Unix-socket HTTP service and routes.
- `internal/agent/bootstrap.go`: current ZFS interface and shared dataset types.
- `cmd/floatlab-agent/main.go`: daemon flags and lifecycle.
- `api/openapi/daemon.yaml`: internal daemon contract.
- `api/openapi/store.yaml`: management storage API contract.
- `deployments/floatlab-system/docker-compose.yaml`: VictoriaMetrics and management container wiring.

Preserve the existing bootstrap behavior for `floatlab/system`, `floatlab/system/logs`, and `floatlab/system/metrics`.

## Architecture

### Management container

- Calls the host agent through `/run/floatlab/agent.sock` using an HTTP client with a Unix-socket transport.
- Exposes or consumes the same ZFS resource contracts at the management layer.
- Relays a source node's send response body directly into a destination node's receive request body.
- Owns future authentication, authorization, TLS, routing, scheduling, and replication policy.

### Host daemon

- Runs privileged ZFS commands without invoking a shell.
- Supports every ZFS pool and dataset visible on the host.
- Validates all dataset, snapshot, property, and boolean inputs before executing commands.
- Streams send/receive data without buffering the complete stream in memory or on disk.
- Collects host metrics every five seconds and pushes them directly to VictoriaMetrics.

## Shared Types

Add request and response types under `pkg/store` so the daemon client and management code use one wire contract.

### Dataset

```go
type Dataset struct {
	Name              string  `json:"name"`
	Type              string  `json:"type"`
	Mountpoint        string  `json:"mountpoint,omitempty"`
	UsedBytes         uint64  `json:"usedBytes"`
	AvailableBytes    uint64  `json:"availableBytes"`
	ReferencedBytes   uint64  `json:"referencedBytes"`
	SnapshotUsedBytes uint64  `json:"snapshotUsedBytes"`
	QuotaBytes        *uint64 `json:"quotaBytes,omitempty"`
}

type CreateDatasetRequest struct {
	Quota       string `json:"quota,omitempty"`
	Compression string `json:"compression,omitempty"`
	Recordsize  string `json:"recordsize,omitempty"`
}
```

`QuotaBytes` is omitted when the ZFS quota is `none`. Creation does not implicitly create missing parents.

### Snapshot

```go
type Snapshot struct {
	Name         string    `json:"name"`
	Dataset      string    `json:"dataset"`
	Snapshot     string    `json:"snapshot"`
	CreatedAt    time.Time `json:"createdAt"`
	UsedBytes    uint64    `json:"usedBytes"`
	ReferencedBytes uint64 `json:"referencedBytes"`
}
```

The caller supplies snapshot names. The daemon does not generate scheduling or retention names.

### Errors

Use the existing JSON shape:

```json
{"error":"message"}
```

Map errors consistently:

- `400 Bad Request`: malformed path, name, property, or incompatible options.
- `404 Not Found`: requested dataset or snapshot does not exist.
- `409 Conflict`: dataset busy, dependent clones, existing resource, or receive history conflict.
- `500 Internal Server Error`: command startup, parsing, or unexpected ZFS failure.

Do not return successful responses when a ZFS command exits unsuccessfully.

## HTTP API

Dataset identifiers are part of the URL path. The Go server uses terminal catch-all wildcards such as `{dataset...}` so embedded `/` characters remain path separators.

### Datasets

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/zfs/dataset` | List every filesystem and volume on the host. |
| `GET` | `/zfs/dataset/{dataset...}` | Return one dataset. Example: `/zfs/dataset/floatlab/app/db`. |
| `PUT` | `/zfs/dataset/{dataset...}` | Create a filesystem using optional core properties from the JSON body. |
| `DELETE` | `/zfs/dataset/{dataset...}?recursive=false` | Delete a dataset; add `zfs destroy -r` only when explicitly requested. |

Dataset listing uses:

```text
zfs list -H -p -t filesystem,volume \
  -o name,type,mountpoint,used,available,referenced,usedbysnapshots,quota
```

Dataset creation uses `zfs create` with only these optional property keys:

- `quota`
- `compression`
- `recordsize`

Pass each property as a separate `-o key=value` argument. Do not accept an arbitrary property map.

### Snapshots

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/zfs/snapshots/{dataset...}?recursive=false` | List snapshots for the dataset, optionally including descendants. |
| `PUT` | `/zfs/snapshot/{dataset...}@{snapshot}?recursive=false` | Create the named snapshot; use `zfs snapshot -r` only when requested. |
| `DELETE` | `/zfs/snapshot/{dataset...}@{snapshot}?recursive=false` | Delete the named snapshot; use recursive destruction only when requested. |

Parse snapshot paths at the final `@`. Dataset validation rejects `@`; snapshot validation rejects `/` and `@`.

Snapshot listing uses numeric, machine-readable output containing name, creation epoch, used bytes, and referenced bytes. Sort using ZFS creation order rather than sorting formatted timestamps in Go.

### Send and receive

| Method | Path | Behavior |
| --- | --- | --- |
| `GET` | `/zfs/send/{dataset...}@{snapshot}?from={baseSnapshot}&recursive=false` | Stream a full or incremental snapshot as `application/octet-stream`. |
| `PUT` | `/zfs/receive/{dataset...}?forceRollback=false` | Stream the request body into `zfs receive`. |

Command mapping:

```text
# Full
zfs send dataset@snapshot

# Incremental
zfs send -i dataset@base dataset@snapshot

# Recursive full or incremental
zfs send -R ...

# Receive
zfs receive dataset

# Explicit destructive rollback
zfs receive -F dataset
```

The `from` parameter is a snapshot component from the same source dataset, not an arbitrary dataset path. Verify that both snapshots exist before starting an incremental send.

Send behavior:

- Start `zfs send` before writing response headers so startup failures retain an HTTP error status.
- Connect command stdout directly to the response writer.
- Capture bounded stderr for error reporting and logs.
- Kill the command when the request context is cancelled.
- A truncated response is a failed stream even if headers were already sent.

Receive behavior:

- Connect the request body directly to command stdin.
- Wait for `zfs receive` to finish before returning success.
- Return `204 No Content` only after a zero exit status.
- Kill the command and close stdin when the upload disconnects or the context is cancelled.
- Never enable `-F` unless `forceRollback=true` was supplied.

The management relay opens the source send, supplies its body as the destination receive body, closes both bodies, and reports success only when both HTTP operations complete successfully. Do not retry a partially consumed stream automatically.

## Input Validation

Because every host dataset is manageable, validation prevents argument confusion rather than enforcing a pool allowlist.

- Reject empty names, leading `-`, control characters, whitespace, empty path components, `.` components, and `..` components.
- Permit only the documented ZFS name character set needed by FloatLab: ASCII letters, digits, `_`, `-`, `.`, and `:` within components.
- Dataset paths may contain `/` but not `@` or `#`.
- Snapshot components may not contain `/`, `@`, or `#`.
- Validate booleans strictly; invalid values return `400` rather than silently becoming false.
- Accept only `quota`, `compression`, and `recordsize` properties.
- Pass arguments through `exec.CommandContext`; never concatenate a shell command.
- Limit captured command stderr so an unexpected command cannot grow memory without bound.

## VictoriaMetrics Export

Add daemon configuration:

- `-node-id`: required stable FloatLab node identifier.
- `-victoria-metrics-url`: default `http://127.0.0.1:8428`.
- `-metrics-interval`: default `5s`.
- `-forward-metrics`: default `true`.

POST Prometheus exposition text to:

```text
{victoria-metrics-url}/api/v1/import/prometheus
```

Run one collection immediately at startup, then sequentially on the interval. Use an HTTP timeout shorter than the interval. Log collection or push failures and retry on the next tick; do not persist failed batches.

Every metric name starts with `fl_`, and every series includes `node_id`.

### Dataset usage metrics

Collect from numeric `zfs list -H -p` output:

- `fl_zfs_dataset_used_bytes{node_id,dataset,pool,type}`
- `fl_zfs_dataset_available_bytes{node_id,dataset,pool,type}`
- `fl_zfs_dataset_referenced_bytes{node_id,dataset,pool,type}`
- `fl_zfs_dataset_snapshot_used_bytes{node_id,dataset,pool,type}`
- `fl_zfs_dataset_quota_bytes{node_id,dataset,pool,type}`, omitted when quota is `none`.

### ZFS pool IO metrics

Collect cumulative logical pool counters from `/proc/spl/kstat/zfs/<pool>/iostats`:

- `fl_zfs_pool_read_operations_total{node_id,pool}`
- `fl_zfs_pool_write_operations_total{node_id,pool}`
- `fl_zfs_pool_read_bytes_total{node_id,pool}`
- `fl_zfs_pool_write_bytes_total{node_id,pool}`

Skip a pool with a warning when its iostat file disappears during collection. Continue exporting other pools and physical disks.

### Physical disk IO metrics

Collect cumulative counters from `/proc/diskstats`. Convert sectors to bytes using 512 bytes per sector and milliseconds to seconds.

- `fl_disk_reads_completed_total{node_id,device,major,minor}`
- `fl_disk_writes_completed_total{node_id,device,major,minor}`
- `fl_disk_read_bytes_total{node_id,device,major,minor}`
- `fl_disk_write_bytes_total{node_id,device,major,minor}`
- `fl_disk_read_seconds_total{node_id,device,major,minor}`
- `fl_disk_write_seconds_total{node_id,device,major,minor}`
- `fl_disk_io_now{node_id,device,major,minor}`
- `fl_disk_io_seconds_total{node_id,device,major,minor}`
- `fl_disk_io_weighted_seconds_total{node_id,device,major,minor}`

Export discard and flush counters with `fl_disk_*` names when the running kernel supplies those fields. Parse by documented field position and tolerate older, shorter records.

Encode labels and floating-point values with the Go standard library. Do not add a Prometheus client dependency solely to format this payload.

## Step-By-Step Implementation

1. **Create shared contracts.** Add dataset, snapshot, request, and error types to `pkg/store`; change the existing daemon dataset responses from human-readable sizes to numeric bytes.
2. **Add validation.** Implement and unit-test dataset path, snapshot component, core property, and strict boolean validation before adding command execution paths.
3. **Extend dataset commands.** Add list-all, get, create, and delete operations to `CommandZFS`, retaining the existing bootstrap methods.
4. **Implement dataset routes.** Register exact `/zfs/dataset` and catch-all `/zfs/dataset/{dataset...}` handlers with method-specific behavior and error mapping.
5. **Add snapshot commands.** Implement list, create, and delete with recursive mode disabled by default.
6. **Implement snapshot routes.** Parse the terminal `dataset@snapshot` resource, validate both parts, and expose list/create/delete handlers.
7. **Implement snapshot send.** Pipe `zfs send` stdout to HTTP, support same-dataset incremental bases and explicit recursive sends, and propagate cancellation.
8. **Implement snapshot receive.** Pipe HTTP request bodies to `zfs receive`, require explicit forced rollback, and return success only after command completion.
9. **Build the Unix client.** Add a `pkg/store` client using `http.Transport.DialContext` for the daemon socket; preserve streaming bodies instead of reading them eagerly.
10. **Add relay primitives.** Let management code connect a source client's send body directly to a destination client's receive call, with no temporary file or complete-memory buffer.
11. **Implement metrics parsers.** Add focused parsers for dataset output, ZFS pool kstats, and variable-length `/proc/diskstats` records.
12. **Implement metric encoding.** Produce the specified `fl_` Prometheus lines with deterministic labels and no external metrics library.
13. **Add the metrics loop.** Run immediate and five-second collections, POST to VictoriaMetrics, avoid overlapping runs, and stop cleanly with the daemon context.
14. **Wire configuration.** Add agent CLI flags, validate the required node ID, and update deployment values so the host reaches the published VictoriaMetrics port.
15. **Update API documentation.** Fully describe path-based dataset resources, binary bodies, recursive/force query flags, schemas, status codes, and examples in both OpenAPI documents.
16. **Update operator documentation.** Document daemon invocation, metric configuration, example dataset calls, and a full source-to-destination stream example.
17. **Run unit checks.** Format changed Go files and run `GOCACHE=/tmp/floatlab-go-cache go test ./...`.
18. **Run VM integration checks.** Execute the acceptance scenarios below against the existing Ubuntu/ZFS development VM.

## Tests And Acceptance Criteria

### Unit tests

- Dataset paths with nested components route correctly, including `floatlab/app/db`.
- Invalid, empty, traversal-like, whitespace, `@`, and leading-dash names are rejected before command execution.
- Dataset output parses numeric bytes and `quota=none` correctly.
- Creation generates only the requested whitelisted `-o` arguments.
- Recursive and force flags are absent by default and present only when explicitly true.
- Snapshot paths split at the final `@`; invalid snapshot components fail.
- Full, incremental, and recursive sends select the expected command arguments.
- Send and receive cancellation terminates their command contexts.
- Receive does not return `204` when the command fails.
- Diskstats parsing supports base, discard, and flush field variants.
- ZFS kstats parsing handles multiple pools and a disappearing pool file.
- Every emitted metric starts with `fl_` and contains `node_id`.
- VictoriaMetrics receives the expected path, content type, and metric body.
- A failed metrics push does not stop later collection attempts.

### VM integration tests

1. Create `floatlab/test/source` with quota, compression, and recordsize options.
2. Fetch it through `GET /zfs/dataset/floatlab/test/source` and confirm numeric properties.
3. Create `floatlab/test/source@full`, send it, and receive it as `floatlab/test/destination`.
4. Modify source data, create `@incremental`, and relay an incremental send from `full`.
5. Confirm source and destination snapshots and data match.
6. Create child datasets and verify recursive snapshot/send behavior only when requested.
7. Confirm non-recursive deletion fails when children or dependent snapshots exist.
8. Confirm explicit recursive deletion removes the test tree.
9. Interrupt a transfer and verify neither handler reports success.
10. Query VictoriaMetrics and confirm `fl_zfs_dataset_*`, `fl_zfs_pool_*`, and `fl_disk_*` series exist with the configured `node_id`.

## Explicitly Deferred

- Raw encrypted sends.
- Resumable receive tokens.
- ZFS bookmarks and holds.
- Snapshot scheduling and retention.
- Replication job persistence or history.
- Automatic stream retries.
- Pool creation, import, export, scrub, or disk replacement.
- Arbitrary ZFS property mutation.
- Inter-node authentication, authorization, certificates, and routing.

Add deferred features only when the management and FloatLab Connect layers require them.
