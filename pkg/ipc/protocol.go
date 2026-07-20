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
	DatasetPath   string `json:"dataset_path"`
	RemoveVolumes bool   `json:"remove_volumes"`
}

type ComposePullPayload struct {
	StackID     string   `json:"stack_id"`
	DatasetPath string   `json:"dataset_path"`
	ComposeFile string   `json:"compose_file"`
	Services    []string `json:"services,omitempty"`
}

type ComposeSourcePayload struct {
	StackID     string `json:"stack_id"`
	DatasetPath string `json:"dataset_path"`
	ComposeFile string `json:"compose_file"`
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

type DatasetSetPayload struct {
	Dataset    string            `json:"dataset"`
	Properties map[string]string `json:"properties"`
}

type DatasetClonePayload struct {
	Snapshot string `json:"snapshot"`
	Target   string `json:"target"`
}

type DatasetRenamePayload struct {
	Source    string `json:"source"`
	Target    string `json:"target"`
	Recursive bool   `json:"recursive,omitempty"`
}

type DatasetPromotePayload struct {
	Dataset string `json:"dataset"`
}

type SnapshotCreatePayload struct {
	Dataset   string `json:"dataset"`
	Name      string `json:"name"`
	Recursive bool   `json:"recursive,omitempty"`
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
	JobID        string `json:"job_id,omitempty"`
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

type VethPayload struct {
	StackID  string `json:"stack_id"`
	HostName string `json:"host_name"`
	PeerName string `json:"peer_name"`
	Address  string `json:"address,omitempty"`
	Bridge   string `json:"bridge"`
}

type DockerEventsPayload struct {
	Subscribe bool `json:"subscribe"`
}

type DockerListPayload struct {
	StackID string `json:"stack_id"`
}

type ContainerInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	State    string `json:"state"`
	Health   string `json:"health"`
	Service  string `json:"service"`
	StackID  string `json:"stack_id"`
	ExitCode int    `json:"exit_code"`
}

type DockerListResult struct {
	Containers []ContainerInfo `json:"containers"`
}

type TerminalOpenPayload struct {
	StackID     string   `json:"stack_id"`
	ContainerID string   `json:"container_id"`
	Command     []string `json:"command,omitempty"`
	Rows        uint     `json:"rows"`
	Cols        uint     `json:"cols"`
}
type TerminalSessionPayload struct {
	SessionID string `json:"session_id"`
}
type TerminalWritePayload struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data"`
}
type TerminalResizePayload struct {
	SessionID string `json:"session_id"`
	Rows      uint   `json:"rows"`
	Cols      uint   `json:"cols"`
}
type TerminalOutputEvent struct {
	SessionID string `json:"session_id"`
	Data      []byte `json:"data,omitempty"`
	Error     string `json:"error,omitempty"`
	Closed    bool   `json:"closed,omitempty"`
}

// Event payloads

type ContainerStateEvent struct {
	ContainerID string `json:"container_id"`
	StackID     string `json:"stack_id"`
	Status      string `json:"status"`
	Service     string `json:"service,omitempty"`
	Image       string `json:"image,omitempty"`
	ExitStatus  string `json:"exit_status,omitempty"`
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

// ZFS list result types — used by fs.pool.list, fs.pool.health, fs.snapshot.list, fs.dataset.list

type PoolSummaryResult struct {
	Name      string `json:"name"`
	Health    string `json:"health"`
	Used      int64  `json:"used"`
	Available int64  `json:"available"`
}

type PoolListResult struct {
	Pools []PoolSummaryResult `json:"pools"`
}

type PoolHealthPayload struct {
	Pool string `json:"pool"`
}

type PoolVDevInfo struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type PoolHealthResult struct {
	Name   string         `json:"name"`
	Health string         `json:"health"`
	VDevs  []PoolVDevInfo `json:"vdevs"`
	Errors string         `json:"errors"`
}

type SnapshotListPayload struct {
	Dataset string `json:"dataset"`
}

type SnapshotInfoResult struct {
	Name      string `json:"name"`
	Dataset   string `json:"dataset"`
	Used      int64  `json:"used"`
	CreatedAt string `json:"created_at"`
}

type SnapshotListResult struct {
	Snapshots []SnapshotInfoResult `json:"snapshots"`
}

type DatasetListPayload struct {
	Parent string `json:"parent"`
}

type DatasetInfoResult struct {
	Name       string `json:"name"`
	Used       int64  `json:"used"`
	Available  int64  `json:"available"`
	Quota      int64  `json:"quota"`
	Mountpoint string `json:"mountpoint"`
}

type DatasetListResult struct {
	Datasets []DatasetInfoResult `json:"datasets"`
}
