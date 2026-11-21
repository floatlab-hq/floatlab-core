package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ZFSStore manages ZFS datasets, snapshots, and pool health via os/exec.
// No CGo required; all operations shell out to the zfs(8) and zpool(8) binaries.
type ZFSStore interface {
	DatasetCreate(ctx context.Context, params DatasetParams) error
	DatasetDestroy(ctx context.Context, dataset string, recursive bool) error
	DatasetList(ctx context.Context, parent string) ([]DatasetInfo, error)
	SnapshotCreate(ctx context.Context, dataset, name string) error
	SnapshotDestroy(ctx context.Context, dataset, name string) error
	SnapshotList(ctx context.Context, dataset string) ([]SnapshotInfo, error)
	SendIncremental(ctx context.Context, params SendParams) (*SendJob, error)
	PoolHealth(ctx context.Context, pool string) (*PoolStatus, error)
	PoolList(ctx context.Context) ([]PoolSummary, error)
}

type DatasetParams struct {
	Dataset     string
	BlockSize   string // "4k", "8k", "32k", "128k"
	Compression string // "none", "lz4", "gzip", "zstd"
	Quota       string // ZFS quota syntax, e.g. "50G"; empty = no quota
	Mountpoint  string // empty = default ZFS-managed
}

type DatasetInfo struct {
	CreatedAt  string
	Name       string
	Used       int64
	Available  int64
	Quota      int64
	Mountpoint string
}

type SnapshotInfo struct {
	Purpose         string   `json:"purpose,omitempty"`
	OperationID     string   `json:"operation_id,omitempty"`
	HoldTargets     []string `json:"hold_targets,omitempty"`
	TransferTargets []string `json:"transfer_targets,omitempty"`
	Holds           []string `json:"holds,omitempty"`
	CreateTXG       int64
	Name            string
	Dataset         string
	Used            int64
	CreatedAt       string
}

type SendParams struct {
	Generation   int64
	SourceNodeID string
	DestNodeID   string
	DestDataset  string
	Policy       string
	Dataset      string
	Snapshot     string
	BaseSnapshot string // empty = full send
	DestHost     string
	DestPort     int
	JobID        string
}

type SendJob struct {
	JobID string
}

type PoolStatus struct {
	Name   string
	Health string // "ONLINE", "DEGRADED", "FAULTED", etc.
	VDevs  []VDevInfo
	Errors string
	Scrub  *ScrubStatus
}

type ScrubStatus struct {
	State         string     `json:"state"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	ProgressPct   *float64   `json:"progress_pct,omitempty"`
	RepairedBytes uint64     `json:"repaired_bytes"`
	Errors        uint64     `json:"errors"`
}

type VDevInfo struct {
	Name  string
	State string
	Read  int64
	Write int64
	CkSum int64
}

type PoolSummary struct {
	Name      string
	Health    string
	Used      int64
	Available int64
}

type zfsStore struct{}

func New() ZFSStore { return &zfsStore{} }

func run(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("store: %s %v: %w: %s", name, args, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (s *zfsStore) DatasetCreate(ctx context.Context, p DatasetParams) error {
	args := []string{"create"}
	if p.BlockSize != "" {
		args = append(args, "-o", "recordsize="+p.BlockSize)
	}
	if p.Compression != "" {
		args = append(args, "-o", "compression="+p.Compression)
	}
	if p.Quota != "" {
		args = append(args, "-o", "quota="+p.Quota)
	}
	if p.Mountpoint != "" {
		args = append(args, "-o", "mountpoint="+p.Mountpoint)
	}
	args = append(args, p.Dataset)
	_, err := run(ctx, "zfs", args...)
	return err
}

func (s *zfsStore) DatasetDestroy(ctx context.Context, dataset string, recursive bool) error {
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, dataset)
	_, err := run(ctx, "zfs", args...)
	return err
}

func (s *zfsStore) DatasetList(ctx context.Context, parent string) ([]DatasetInfo, error) {
	out, err := run(ctx, "zfs", "list", "-H", "-p", "-o", "name,used,avail,quota,mountpoint,creation", "-r", parent)
	if err != nil {
		return nil, err
	}
	var result []DatasetInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 6 {
			continue
		}
		result = append(result, DatasetInfo{
			Name:       parts[0],
			Used:       parseInt64(parts[1]),
			Available:  parseInt64(parts[2]),
			Quota:      parseInt64(parts[3]),
			Mountpoint: parts[4],
			CreatedAt:  time.Unix(parseInt64(parts[5]), 0).UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (s *zfsStore) SnapshotCreate(ctx context.Context, dataset, name string) error {
	_, err := run(ctx, "zfs", "snapshot", dataset+"@"+name)
	return err
}

func (s *zfsStore) SnapshotDestroy(ctx context.Context, dataset, name string) error {
	_, err := run(ctx, "zfs", "destroy", dataset+"@"+name)
	return err
}

func (s *zfsStore) SnapshotList(ctx context.Context, dataset string) ([]SnapshotInfo, error) {
	out, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,used,creation,createtxg", "-r", dataset)
	if err != nil {
		return nil, err
	}
	var result []SnapshotInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		nameparts := strings.SplitN(parts[0], "@", 2)
		snap := SnapshotInfo{
			CreateTXG: parseInt64(parts[3]),
			Dataset:   nameparts[0],
			Used:      parseInt64(parts[1]),
			CreatedAt: time.Unix(parseInt64(parts[2]), 0).UTC().Format(time.RFC3339),
		}
		if len(nameparts) == 2 {
			snap.Name = nameparts[1]
		}
		if IsAutomatedSnapshot(snap.Name) {
			for property, dest := range map[string]*string{"floatlab:purpose": &snap.Purpose, "floatlab:operation-id": &snap.OperationID} {
				value, e := run(ctx, "zfs", "get", "-H", "-o", "value", property, parts[0])
				if e != nil {
					return nil, e
				}
				if value != "-" {
					*dest = value
				}
			}
			for property, dest := range map[string]*[]string{HoldTargetsProperty: &snap.HoldTargets, TransferTargetsProperty: &snap.TransferTargets} {
				value, e := run(ctx, "zfs", "get", "-H", "-o", "value", property, parts[0])
				if e != nil {
					return nil, e
				}
				if value != "-" && value != "" {
					if e = json.Unmarshal([]byte(value), dest); e != nil {
						return nil, fmt.Errorf("invalid snapshot protection metadata on %s: %w", parts[0], e)
					}
				}
			}
			holds, e := snapshotHolds(ctx, parts[0])
			if e != nil {
				return nil, e
			}
			for tag := range holds {
				snap.Holds = append(snap.Holds, tag)
			}
		}
		result = append(result, snap)
	}
	return result, nil
}

// SendIncremental verifies a resumable transfer through the restricted lab SSH receiver.
func (s *zfsStore) SendIncremental(ctx context.Context, p SendParams) (*SendJob, error) {
	_, err := Replicate(ctx, ReplicationRequest{Generation: p.Generation, Dataset: p.Dataset, Snapshot: p.Snapshot, SourceNodeID: p.SourceNodeID, DestNodeID: p.DestNodeID, DestDataset: p.DestDataset, DestHost: p.DestHost, DestPort: p.DestPort, Policy: p.Policy, JobID: p.JobID})
	if err != nil {
		return nil, err
	}
	return &SendJob{JobID: p.JobID}, nil
}

func (s *zfsStore) PoolHealth(ctx context.Context, pool string) (*PoolStatus, error) {
	out, err := run(ctx, "zpool", "status", "-jP", "--json-int", "--", pool)
	if err == nil {
		return parsePoolStatusJSON(out, pool)
	}
	// OpenZFS releases before 2.3 only support text status.
	out, err = run(ctx, "zpool", "status", "-P", "--", pool)
	if err != nil {
		return nil, err
	}
	status := &PoolStatus{Name: pool}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state:") {
			status.Health = strings.TrimSpace(strings.TrimPrefix(line, "state:"))
		}
		if strings.HasPrefix(line, "errors:") {
			status.Errors = strings.TrimSpace(strings.TrimPrefix(line, "errors:"))
		}
	}
	return status, nil
}

func parsePoolStatusJSON(out, pool string) (*PoolStatus, error) {
	var response struct {
		Pools map[string]struct {
			Health     string  `json:"state"`
			ErrorCount *uint64 `json:"error_count"`
			Scan       struct {
				Function string `json:"function"`
				State    string `json:"state"`
				Start    int64  `json:"start_time"`
				End      int64  `json:"end_time"`
				Pause    int64  `json:"scrub_pause"`
				Issued   uint64 `json:"issued"`
				Total    uint64 `json:"to_examine"`
				Repaired uint64 `json:"processed"`
				Errors   uint64 `json:"errors"`
			} `json:"scan_stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		return nil, fmt.Errorf("decode zpool status: %w", err)
	}
	data, ok := response.Pools[pool]
	if !ok || data.Health == "" {
		return nil, fmt.Errorf("zpool status missing pool health for %q", pool)
	}
	status := &PoolStatus{Name: pool, Health: data.Health}
	if data.ErrorCount != nil {
		if *data.ErrorCount == 0 {
			status.Errors = "No known data errors"
		} else {
			status.Errors = fmt.Sprintf("%d data errors, use '-v' for a list", *data.ErrorCount)
		}
	}
	scan := data.Scan
	if scan.Function != "SCRUB" {
		return status, nil
	}
	scrub := &ScrubStatus{RepairedBytes: scan.Repaired, Errors: scan.Errors}
	switch scan.State {
	case "SCANNING":
		scrub.State = "running"
		if scan.Pause != 0 {
			scrub.State = "paused"
		}
		if scan.Total > 0 {
			progress := min(100, 100*float64(scan.Issued)/float64(scan.Total))
			scrub.ProgressPct = &progress
		}
	case "FINISHED":
		scrub.State = "completed"
		if scan.End > 0 {
			completed := time.Unix(scan.End, 0).UTC()
			scrub.CompletedAt = &completed
		}
	case "CANCELED":
		scrub.State = "canceled"
	default:
		return status, nil
	}
	if scan.Start > 0 {
		started := time.Unix(scan.Start, 0).UTC()
		scrub.StartedAt = &started
	}
	status.Scrub = scrub
	return status, nil
}

func (s *zfsStore) PoolList(ctx context.Context) ([]PoolSummary, error) {
	out, err := run(ctx, "zpool", "list", "-H", "-p", "-o", "name,health,alloc,free")
	if err != nil {
		return nil, err
	}
	var result []PoolSummary
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		result = append(result, PoolSummary{
			Name:      parts[0],
			Health:    parts[1],
			Used:      parseInt64(parts[2]),
			Available: parseInt64(parts[3]),
		})
	}
	return result, nil
}

func parseInt64(s string) int64 {
	if s == "-" || s == "none" {
		return 0
	}
	var v int64
	fmt.Sscanf(s, "%d", &v)
	return v
}
