# pkg/config

rqlite-backed configuration store for all FloatLab platform entities.

## Core Types

```go
type Node struct {
    ID            string        `json:"id"`
    Name          string        `json:"name"`
    Hostname      string        `json:"hostname"`
    Addresses     []NodeAddress `json:"addresses"`
    Status        string        `json:"status"`   // online|offline|degraded|unknown
    Role          string        `json:"role"`     // primary|secondary
    ZFSPool       string        `json:"zfs_pool"`
    OSVersion     string        `json:"os_version,omitempty"`
    KernelVersion string        `json:"kernel_version,omitempty"`
    CreatedAt     time.Time     `json:"created_at"`
    UpdatedAt     time.Time     `json:"updated_at"`
}

type NodeAddress struct {
    Type      string `json:"type"`      // LAN-6|WAN-6|Overlay-6
    Address   string `json:"address"`   // IPv6 CIDR notation
    Interface string `json:"interface"`
}

type Stack struct {
    ID                string    `json:"id"`
    Name              string    `json:"name"`
    State             string    `json:"state"`
    ComposeYAML       string    `json:"compose_yaml"`
    PrimaryNodeID     string    `json:"primary_node_id"`
    SecondaryNodeID   string    `json:"secondary_node_id,omitempty"`
    FailoverMode      string    `json:"failover_mode"`       // manual|auto
    AutoTriggerAfter  string    `json:"auto_trigger_after,omitempty"` // e.g. "120s"
    CreatedAt         time.Time `json:"created_at"`
    UpdatedAt         time.Time `json:"updated_at"`
}

type AlertRule struct {
    ID          string    `json:"id"`
    Name        string    `json:"name"`
    Condition   string    `json:"condition"`
    Severity    string    `json:"severity"`
    StackID     string    `json:"stack_id,omitempty"`
    NodeID      string    `json:"node_id,omitempty"`
    ForDuration string    `json:"for_duration,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty"`
    Enabled     bool      `json:"enabled"`
    CreatedAt   time.Time `json:"created_at"`
    UpdatedAt   time.Time `json:"updated_at"`
}
```

## rqlite Tables

`nodes`, `stacks`, `networks`, `ip_reservations`, `alert_rules`, `alerts` — schema is defined in `pkg/rqlite/migrations.go` and applied at startup.

## CRUD Interface

Each entity type exposes `List`, `Get`, `Create`, `Update`, `Delete` methods backed by rqlite parameterised queries. All writes go through the Raft leader via rqlite's leader-redirect.

## Change Watch Channel

```go
type ChangeEvent struct {
    Kind   string // "node" | "stack" | "alert_rule"
    Action string // "created" | "updated" | "deleted"
    ID     string
}

func (s *Store) Watch() <-chan ChangeEvent
```

Internal components (orchestrator, failover detector) subscribe to `Watch()` to react to configuration changes without polling. The channel is closed when the Store is shut down.

Auth is **not** stored here — identity comes from Pangolin `Remote-User`/`Remote-Email` headers. The Users table from the original design is excluded from MVP (DEV-006).
