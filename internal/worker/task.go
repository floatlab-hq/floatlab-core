package worker

import "encoding/json"

// Task type constants match the tasks.type column in rqlite.
const (
	TaskSnapshotCreate = "snapshot.create"
	TaskSnapshotDelete = "snapshot.delete"
	TaskReplTrigger    = "repl.trigger"
)

// SnapshotCreatePayload is the JSON body stored in tasks.payload for snapshot.create.
type SnapshotCreatePayload struct {
	Dataset  string `json:"dataset"`
	SnapType string `json:"snap_type"` // "hourly", "daily", "weekly", "monthly", "user"
	Label    string `json:"label,omitempty"`
	Keep     int    `json:"keep"`
}

// SnapshotDeletePayload is the JSON body stored in tasks.payload for snapshot.delete.
type SnapshotDeletePayload struct {
	Dataset  string `json:"dataset"`
	Snapshot string `json:"snapshot"`
}

// ReplTriggerPayload is the JSON body stored in tasks.payload for repl.trigger.
type ReplTriggerPayload struct {
	StackID  string `json:"stack_id"`
	Dataset  string `json:"dataset"`
	DestHost string `json:"dest_host"`
	DestPort int    `json:"dest_port"`
}

func marshalPayload(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
