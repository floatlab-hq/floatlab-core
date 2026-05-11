package store

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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
	Name       string
	Used       int64
	Available  int64
	Quota      int64
	Mountpoint string
}

type SnapshotInfo struct {
	Name      string
	Dataset   string
	Used      int64
	CreatedAt string
}

type SendParams struct {
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
}

type VDevInfo struct {
	Name   string
	State  string
	Read   int64
	Write  int64
	CkSum  int64
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
	out, err := run(ctx, "zfs", "list", "-H", "-p", "-o", "name,used,avail,quota,mountpoint", "-r", parent)
	if err != nil {
		return nil, err
	}
	var result []DatasetInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 5 {
			continue
		}
		result = append(result, DatasetInfo{
			Name:       parts[0],
			Used:       parseInt64(parts[1]),
			Available:  parseInt64(parts[2]),
			Quota:      parseInt64(parts[3]),
			Mountpoint: parts[4],
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
	out, err := run(ctx, "zfs", "list", "-H", "-p", "-t", "snapshot", "-o", "name,used,creation", "-r", dataset)
	if err != nil {
		return nil, err
	}
	var result []SnapshotInfo
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			continue
		}
		nameparts := strings.SplitN(parts[0], "@", 2)
		snap := SnapshotInfo{
			Dataset:   nameparts[0],
			Used:      parseInt64(parts[1]),
			CreatedAt: parts[2],
		}
		if len(nameparts) == 2 {
			snap.Name = nameparts[1]
		}
		result = append(result, snap)
	}
	return result, nil
}

// SendIncremental initiates a zfs send | ssh zfs recv pipeline.
// The actual send is handled by the hostd via IPC; this method is a stub
// used by the control plane to construct the IPC payload.
func (s *zfsStore) SendIncremental(ctx context.Context, p SendParams) (*SendJob, error) {
	// Real implementation: exec zfs send -i base@snap dataset@snap | ssh dest zfs recv dataset
	// For Sprint 1: return job placeholder; full implementation in Sprint 2.
	return &SendJob{JobID: p.JobID}, nil
}

func (s *zfsStore) PoolHealth(ctx context.Context, pool string) (*PoolStatus, error) {
	out, err := run(ctx, "zpool", "status", "-P", pool)
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
