# pkg/stats

VictoriaMetrics remote-write client, metric query proxy, and vmalert webhook receiver.

## Sidecar Layout

| Container | Host | Port |
|-----------|------|------|
| VictoriaMetrics | `metrics` | 8428 |
| vmagent | `vmagent` | 8429 |

Data is stored in `floatlab/floatlab-stats` at 5s resolution for 45 days (`--retentionPeriod=45d`). ARC and memory footprint are bounded with `-memory.allowedPercent=60` and `GOGC=40`.

## Metric Collection

vmagent scrapes:
- **cadvisor** on each node: container CPU, memory, network, block I/O
- **node_exporter** on each node: host CPU, RAM, disk I/O, network
- **floatlab-hostd** `/metrics` endpoint: ZFS pool/dataset stats, replication byte counters

All series are labeled with `stack_id`, `node_id`, `container_name`, `service`.

## Remote-Write Client (`client.go`)

Pushes custom FloatLab-specific metrics to VictoriaMetrics using Prometheus protobuf remote-write (`POST /api/v1/write`). Used by the control plane to record:
- `floatlab_stack_state` gauge (mapped from StackState enum)
- `floatlab_failover_duration_seconds` histogram
- `floatlab_replication_lag_seconds` gauge
- `floatlab_snapshot_count` gauge

```go
type Point struct {
    Name   string
    Labels map[string]string
    Value  float64
    TS     time.Time
}

func (c *Client) Write(ctx context.Context, points []Point) error
```

## Query Proxy (`query.go`)

Wraps VictoriaMetrics `/api/v1/query_range` with a typed response deserialiser. Returns `[]MetricSeries` where each series is `{Name, Labels, Points []MetricPoint{TS int64, Value float64}}`.

Pre-built scoped queries for the REST API:
- `NodeQuery(nodeID, metric, start, end, step)` — adds `{node_id="<id>"}` label filter
- `StackQuery(stackID, metric, ...)` — adds `{stack_id="<id>"}` label filter

## Alert evaluation and external webhook

Management and Compose rules are evaluated inside the management service; see
[the evaluator documentation](../alerts/README.md). No built-in rules are seeded
by this package. The default Compose deployment no longer runs vmalert.

The external Alertmanager-compatible receiver remains at
`POST /api/v1/stats/webhook` for separately owned external rules. It persists
firing/resolved state and notifies on first firing. It does not look up or evaluate
management rule definitions. Do not run external and management evaluation for
the same rule. External alerts retain their existing identity/history behaviour.
