# pkg/notify

Persistent notification store backed by rqlite and real-time SSE fan-out broker.

## Notification Model

```go
type Severity string
const (
    SeverityInfo     Severity = "info"
    SeverityWarning  Severity = "warning"
    SeverityCritical Severity = "critical"
)

type State string
const (
    StateUnread   State = "unread"
    StateRead     State = "read"
    StateSilenced State = "silenced"
    StateResolved State = "resolved"
)

type Notification struct {
    ID           string    `json:"id"`
    AlertID      string    `json:"alert_id,omitempty"`
    Kind         string    `json:"kind"`           // e.g. "failover.complete"
    Severity     Severity  `json:"severity"`
    State        State     `json:"state"`
    Title        string    `json:"title"`
    Body         string    `json:"body"`
    StackID      string    `json:"stack_id,omitempty"`
    NodeID       string    `json:"node_id,omitempty"`
    SilencedUntil *time.Time `json:"silenced_until,omitempty"`
    ResolvedAt   *time.Time `json:"resolved_at,omitempty"`
    CreatedAt    time.Time  `json:"created_at"`
}
```

## rqlite Schema

```sql
CREATE TABLE notifications (
    id            TEXT PRIMARY KEY,
    alert_id      TEXT,
    kind          TEXT,
    severity      TEXT NOT NULL,
    state         TEXT NOT NULL DEFAULT 'unread',
    title         TEXT NOT NULL,
    body          TEXT NOT NULL,
    stack_id      TEXT,
    node_id       TEXT,
    silenced_until DATETIME,
    resolved_at   DATETIME,
    created_at    DATETIME NOT NULL
);
CREATE INDEX idx_notifications_state ON notifications(state);
CREATE INDEX idx_notifications_stack ON notifications(stack_id);
```

## SSE Fan-out Broker

`broker.go` maintains a map of subscriber channels (one per connected SSE client). On `Publish(n Notification)`:
1. Write to rqlite
2. Broadcast to all subscriber channels (non-blocking send; slow clients are dropped after 1s)

Subscribers register via `broker.Subscribe() (<-chan Notification, CancelFunc)`.

The control plane `/api/v1/events` SSE endpoint consumes this channel and encodes events as:
```
event: notification.new
data: <JSON Notification>
```

## Retention Cleanup

A background goroutine in `store.go` runs every 24 hours and deletes notifications where:
```sql
state = 'resolved' AND resolved_at < datetime('now', '-30 days')
```

## Silence vs Resolve

- **Silence**: sets `state = silenced`, `silenced_until = <requested time>`. A scheduled check at `silenced_until` resets state to `unread` if not resolved.
- **Resolve**: sets `state = resolved`, `resolved_at = now()`. Triggers a `notification.resolved` SSE event.
