# pkg/logs

VictoriaLogs push client, SSE tail proxy, and LogsQL query wrapper.

## Architecture

A VictoriaLogs sidecar runs in the `floatlab-logs` dataset at `http://logs:9428`.
All container logs are shipped via Docker's JSON-file log driver by a vmagent scrape job.
Host daemon logs (hostd, journald) are pushed directly by this package using the VictoriaLogs HTTP ingestion API.

## Go Types

```go
type LogLine struct {
    TS     time.Time         `json:"ts"`
    Stream string            `json:"stream"`  // "stdout" | "stderr"
    Level  string            `json:"level"`   // may be empty
    Msg    string            `json:"msg"`
    Labels map[string]string `json:"labels"`  // stack_id, container_id, service, node_id
}

type AuditEntry struct {
    ID           string    `json:"id"`
    Actor        string    `json:"actor"`         // Pangolin Remote-User
    Action       string    `json:"action"`        // e.g. "stack.failover.trigger"
    ResourceType string    `json:"resource_type"`
    ResourceID   string    `json:"resource_id"`
    Detail       string    `json:"detail"`        // JSON blob, may be empty
    TS           time.Time `json:"ts"`
}
```

## Push Format

VictoriaLogs accepts JSON streams via `POST /insert/jsonline`:

```json
{"_time":"2024-01-01T12:00:00Z","_msg":"container started","stack_id":"abc","service":"db","stream":"stdout"}
```

Labels `stack_id`, `container_id`, `service`, `node_id` are written as top-level fields — VictoriaLogs indexes them as stream labels automatically.

## SSE Tail Proxy

`tail.go` opens a chunked HTTP connection to VictoriaLogs `/select/logsql/tail?query=...` and re-emits lines to the control plane SSE handler. The caller receives `data: <JSON LogLine>` events.

Connection lifecycle: the proxy goroutine exits when the client SSE connection closes (context cancellation).

## LogsQL Query Wrapper

`query.go` wraps `/select/logsql/query` with typed parameters and deserialises the NDJSON response into `[]LogLine`. The `since` parameter maps to VictoriaLogs `_time` range filter.

## Audit Log

Audit entries are pushed to VictoriaLogs with `_stream:{kind="audit"}` and read back via the standard LogsQL query path. Control plane middleware emits an `AuditEntry` for every mutating API request using the Pangolin `Remote-User` header.

## Retention

VictoriaLogs is configured with `--retentionPeriod=30d`. Stack log streams are labeled with `stack_id`; when a stack is deleted the control plane calls `/internal/delete?query=_stream:{stack_id="<id>"}` to purge its logs.
