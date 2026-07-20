package compose

// StackExtension holds the x-fl-stack top-level compose extension.
type StackExtension struct {
	ID            string            `json:"id,omitempty"`
	SchemaVersion int               `json:"schema_version"`
	PrimaryNode   string            `json:"primary_node"`
	SecondaryNode string            `json:"secondary_node,omitempty"`
	Failover      FailoverConfig    `json:"failover"`
	Snapshots     SnapshotConfig    `json:"snapshots"`
	Replication   ReplConfig        `json:"replication"`
	Backup        BackupConfig      `json:"backup"`
	Storage       StorageConfig     `json:"storage"`
	Labels        map[string]string `json:"labels,omitempty"`
}

type FailoverConfig struct {
	Mode             string `json:"mode"` // "manual" | "auto"
	AutoTriggerAfter string `json:"auto_trigger_after,omitempty"`
}

type SnapshotSchedule struct {
	Keep int `json:"keep"`
}

type SnapshotScheduleConfig struct {
	Hourly  SnapshotSchedule `json:"hourly"`
	Daily   SnapshotSchedule `json:"daily"`
	Weekly  SnapshotSchedule `json:"weekly"`
	Monthly SnapshotSchedule `json:"monthly"`
}

type SnapshotConfig struct {
	Schedule SnapshotScheduleConfig `json:"schedule"`
}

type ReplConfig struct {
	Enabled  bool   `json:"enabled"`
	Schedule string `json:"schedule"` // cron, UTC
}

type BackupConfig struct {
	Enabled       bool   `json:"enabled"`
	Target        string `json:"target,omitempty"`
	Schedule      string `json:"schedule,omitempty"`
	RetentionDays int    `json:"retention_days,omitempty"`
}

type StorageConfig struct {
	Pool        string `json:"pool"`
	BlockSize   string `json:"block_size"`  // "4k"|"8k"|"32k"|"128k"
	Compression string `json:"compression"` // "none"|"lz4"|"gzip"|"zstd"
	Quota       string `json:"quota,omitempty"`
}

// VolumeExtension holds the x-fl-volumes per-service volume overrides.
type VolumeExtension struct {
	BlockSize   string `json:"block_size,omitempty"`
	Compression string `json:"compression,omitempty"`
	Quota       string `json:"quota,omitempty"`
}

// ParsedStack is the fully parsed result of a FloatLab compose file.
type ParsedStack struct {
	Extension      StackExtension
	ServiceVolumes map[string]map[string]VolumeExtension // service → volume → ext
	ProjectName    string
}
