package store

import "time"

type Dataset struct {
	Name              string  `json:"name"`
	Type              string  `json:"type"`
	Mountpoint        string  `json:"mountpoint,omitempty"`
	UsedBytes         uint64  `json:"usedBytes"`
	AvailableBytes    uint64  `json:"availableBytes"`
	ReferencedBytes   uint64  `json:"referencedBytes"`
	SnapshotUsedBytes uint64  `json:"snapshotUsedBytes"`
	QuotaBytes        *uint64 `json:"quotaBytes,omitempty"`
}

type CreateDatasetRequest struct {
	Quota       string `json:"quota,omitempty"`
	Compression string `json:"compression,omitempty"`
	Recordsize  string `json:"recordsize,omitempty"`
}

type Snapshot struct {
	Name            string    `json:"name"`
	Dataset         string    `json:"dataset"`
	Snapshot        string    `json:"snapshot"`
	CreatedAt       time.Time `json:"createdAt"`
	UsedBytes       uint64    `json:"usedBytes"`
	ReferencedBytes uint64    `json:"referencedBytes"`
}
