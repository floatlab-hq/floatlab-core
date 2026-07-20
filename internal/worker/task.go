package worker

import "encoding/json"

// Task type constants match the tasks.type column in rqlite.
const (
	TaskSnapshotCreate = "snapshot.create"
	TaskSnapshotDelete = "snapshot.delete"
	TaskReplTrigger    = "repl.trigger"
	TaskStackUpgrade   = "stack.upgrade"
	TaskStackRestart   = "stack.restart"
	TaskStackDelete    = "stack.delete"
	TaskStackRestore   = "stack.restore"
)

// SnapshotCreatePayload is stored in tasks.payload for snapshot.create tasks.
type SnapshotCreatePayload struct {
	Dataset     string `json:"dataset"`
	NodeID      string `json:"node_id"`
	SnapType    string `json:"snap_type"` // "hourly", "daily", "weekly", "monthly", "user"
	Label       string `json:"label,omitempty"`
	Keep        int    `json:"keep"`
	Recursive   bool   `json:"recursive,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	StackID     string `json:"stack_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

// SnapshotDeletePayload is stored in tasks.payload for snapshot.delete tasks.
type SnapshotDeletePayload struct {
	Dataset         string `json:"dataset"`
	NodeID          string `json:"node_id"`
	Snapshot        string `json:"snapshot"`
	OperationID     string `json:"operation_id,omitempty"`
	StackID         string `json:"stack_id,omitempty"`
	RecoveryDataset string `json:"recovery_dataset,omitempty"`
	Actor           string `json:"actor,omitempty"`
}

// ReplTriggerPayload is stored in tasks.payload for repl.trigger tasks.
type ReplTriggerPayload struct {
	StackID  string `json:"stack_id"`
	Dataset  string `json:"dataset"`
	NodeID   string `json:"node_id"` // primary node that runs zfs send
	DestHost string `json:"dest_host"`
	DestPort int    `json:"dest_port"`
}

type StackUpgradePayload struct {
	OperationID   string   `json:"operation_id"`
	StackID       string   `json:"stack_id"`
	NodeID        string   `json:"node_id"`
	DatasetPath   string   `json:"dataset_path"`
	OldCompose    string   `json:"old_compose"`
	NewCompose    string   `json:"new_compose"`
	Services      []string `json:"services"`
	HealthTimeout string   `json:"health_timeout"`
	Actor         string   `json:"actor,omitempty"`
}

type StackRestartPayload struct {
	OperationID   string `json:"operation_id"`
	StackID       string `json:"stack_id"`
	NodeID        string `json:"node_id"`
	DatasetPath   string `json:"dataset_path"`
	ComposeFile   string `json:"compose_file"`
	HealthTimeout string `json:"health_timeout"`
	Actor         string `json:"actor,omitempty"`
}

type StackDeletePayload struct {
	OperationID string `json:"operation_id"`
	StackID     string `json:"stack_id"`
	NodeID      string `json:"node_id"`
	DatasetPath string `json:"dataset_path"`
	Purge       bool   `json:"purge"`
	Actor       string `json:"actor,omitempty"`
}

type StackRestorePayload struct {
	OperationID   string `json:"operation_id"`
	StackID       string `json:"stack_id"`
	NodeID        string `json:"node_id"`
	DatasetPath   string `json:"dataset_path"`
	Snapshot      string `json:"snapshot"`
	ComposeFile   string `json:"compose_file"`
	SourceCompose string `json:"source_compose"`
	HealthTimeout string `json:"health_timeout"`
	WasRunning    bool   `json:"was_running"`
	Actor         string `json:"actor,omitempty"`
}

func marshalPayload(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
