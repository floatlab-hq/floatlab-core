package store

import (
	"context"
	"fmt"
)

type SendResult struct {
	JobID      string `json:"job_id"`
	BytesSent  int64  `json:"bytes_sent"`
	PruneError string `json:"prune_error,omitempty"`
}

// SendReplication requires stable node identities; callers must use Replicate.
func SendReplication(ctx context.Context, jobID, dataset, snapshot, baseSnapshot, destHost string, destPort int) (*SendResult, error) {
	return nil, fmt.Errorf("replication requires authenticated source and destination node IDs")
}
