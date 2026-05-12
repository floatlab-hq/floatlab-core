package config

import "time"

type Node struct {
	ID          string        `json:"id"`
	ClusterUUID string        `json:"cluster_uuid"`
	Name        string        `json:"name"`
	Addresses   []NodeAddress `json:"addresses"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

type NodeAddress struct {
	Type      string `json:"type"` // "LAN-6", "WAN-6", "Overlay-6"
	Address   string `json:"address"`
	Netmask   string `json:"netmask"`
	NetworkID string `json:"network_id,omitempty"`
}

type Stack struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Icon                string    `json:"icon,omitempty"`
	PrimaryNodeID       string    `json:"primary_node_id"`
	BackupNodeID        string    `json:"backup_node_id,omitempty"`
	ComposeYAML         string    `json:"compose_yaml"`
	ZFSDataset          string    `json:"zfs_dataset"`
	SnapshotSchedule    string    `json:"snapshot_schedule,omitempty"`
	ReplicationSchedule string    `json:"replication_schedule,omitempty"`
	BackupSchedule      string    `json:"backup_schedule,omitempty"`
	BackupTarget        string    `json:"backup_target,omitempty"`
	FailoverMode        string    `json:"failover_mode,omitempty"`      // "manual" | "auto"
	AutoTriggerAfter    string    `json:"auto_trigger_after,omitempty"` // e.g. "120s"
	State               string    `json:"state,omitempty"`              // runtime FSM state (not persisted)
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type Network struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Prefix      string `json:"prefix"` // IPv6 CIDR
	ReservedMin string `json:"reserved_min,omitempty"`
	ReservedMax string `json:"reserved_max,omitempty"`
}

type AlertRule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
	Condition   string `json:"condition"`
	Action      string `json:"action"`
	Channel     string `json:"channel"`
}

type ChangeEvent struct {
	Entity string // "node", "stack", "network", "alert_rule"
	Action string // "create", "update", "delete"
	ID     string
}
