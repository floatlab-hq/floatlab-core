package ipc

import "encoding/json"

type MessageType string

const (
	TypeCommand  MessageType = "command"
	TypeResponse MessageType = "response"
	TypeEvent    MessageType = "event"
)

type Message struct {
	ID      string          `json:"id"`
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type Command struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	ID      string          `json:"id"`
	OK      bool            `json:"ok"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string { return e.Code + ": " + e.Message }

type Event struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Command payloads

type ComposeUpPayload struct {
	StackID     string `json:"stack_id"`
	DatasetPath string `json:"dataset_path"`
	ComposeFile string `json:"compose_file"`
}

type ComposeDownPayload struct {
	StackID       string `json:"stack_id"`
	RemoveVolumes bool   `json:"remove_volumes"`
}

type ComposePullPayload struct {
	StackID  string   `json:"stack_id"`
	Services []string `json:"services,omitempty"`
}

type DatasetCreatePayload struct {
	Dataset     string `json:"dataset"`
	BlockSize   string `json:"block_size"`
	Compression string `json:"compression"`
	Quota       string `json:"quota,omitempty"`
}

type DatasetDestroyPayload struct {
	Dataset   string `json:"dataset"`
	Recursive bool   `json:"recursive"`
}

type SnapshotCreatePayload struct {
	Dataset string `json:"dataset"`
	Name    string `json:"name"`
}

type SnapshotDestroyPayload struct {
	Dataset string `json:"dataset"`
	Name    string `json:"name"`
}

type ReplSendPayload struct {
	Dataset      string `json:"dataset"`
	Snapshot     string `json:"snapshot"`
	BaseSnapshot string `json:"base_snapshot,omitempty"`
	DestHost     string `json:"dest_host"`
	DestPort     int    `json:"dest_port"`
}

type ReplRecvPayload struct {
	Dataset    string `json:"dataset"`
	SourceHost string `json:"source_host"`
	SourcePort int    `json:"source_port"`
	JobID      string `json:"job_id"`
}

type ReplStatusPayload struct {
	JobID string `json:"job_id"`
}

type NetAddrPayload struct {
	Interface string `json:"interface"`
	Address   string `json:"address"`
}

type NetRoutePayload struct {
	Prefix  string `json:"prefix"`
	Nexthop string `json:"nexthop,omitempty"`
}

type DockerEventsPayload struct {
	Subscribe bool `json:"subscribe"`
}

type DockerListPayload struct {
	StackID string `json:"stack_id"`
}

type ContainerInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Health  string `json:"health"`
	Service string `json:"service"`
	StackID string `json:"stack_id"`
}

type DockerListResult struct {
	Containers []ContainerInfo `json:"containers"`
}

// Event payloads

type ContainerStateEvent struct {
	ContainerID string `json:"container_id"`
	StackID     string `json:"stack_id"`
	Status      string `json:"status"`
}

type ContainerHealthEvent struct {
	ContainerID string `json:"container_id"`
	StackID     string `json:"stack_id"`
	Health      string `json:"health"`
}

type ZFSFaultEvent struct {
	Pool   string `json:"pool"`
	VDev   string `json:"vdev"`
	State  string `json:"state"`
	Errors string `json:"errors"`
}

type ReplProgressEvent struct {
	JobID      string `json:"job_id"`
	BytesSent  int64  `json:"bytes_sent"`
	BytesTotal int64  `json:"bytes_total"`
	ETASeconds int    `json:"eta_seconds"`
}

type ReplCompleteEvent struct {
	JobID    string `json:"job_id"`
	Snapshot string `json:"snapshot"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
}

type HostdReadyEvent struct {
	Hostname      string `json:"hostname"`
	UptimeSeconds int64  `json:"uptime_seconds"`
}

type SysInfoResult struct {
	Hostname      string   `json:"hostname"`
	KernelVersion string   `json:"kernel_version"`
	ZFSPools      []string `json:"zfs_pools"`
	DockerVersion string   `json:"docker_version"`
}
