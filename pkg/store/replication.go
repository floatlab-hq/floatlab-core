package store

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const replBufSize = 1 << 20 // 1 MB copy buffer

// LatestReplSnapshot returns the name of the most recent replication snapshot
// (fsrepl-NNNNNN-YYYYMMDD-HHMMSS) from the provided list, or "" if none exist.
func LatestReplSnapshot(snaps []SnapshotInfo) string {
	prefix := PrefixReplication + "-"
	var matching []string
	for _, s := range snaps {
		if strings.HasPrefix(s.Name, prefix) {
			matching = append(matching, s.Name)
		}
	}
	if len(matching) == 0 {
		return ""
	}
	sort.Strings(matching)
	return matching[len(matching)-1]
}

// nextReplSeq derives the next sequence number by incrementing the sequence
// embedded in the most recent replication snapshot name.
// Returns 1 if no prior replication snapshots exist.
func nextReplSeq(snaps []SnapshotInfo) int {
	latest := LatestReplSnapshot(snaps)
	if latest == "" {
		return 1
	}
	// Format: fsrepl-NNNNNN-YYYYMMDD-HHMMSS
	parts := strings.SplitN(latest, "-", 3)
	if len(parts) < 2 {
		return 1
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil {
		return 1
	}
	return n + 1
}

// PlanReplication determines the snapshot names to use for the next replication
// cycle, creates the new snapshot, and returns (newSnap, baseSnap, error).
// baseSnap is empty when a full send is required (no prior replication snapshots).
func PlanReplication(ctx context.Context, zfs ZFSStore, dataset string) (newSnap, baseSnap string, err error) {
	snaps, err := zfs.SnapshotList(ctx, dataset)
	if err != nil {
		return "", "", fmt.Errorf("store: plan replication %s: list: %w", dataset, err)
	}
	baseSnap = LatestReplSnapshot(snaps)
	seq := nextReplSeq(snaps)
	newSnap = ReplicationSnapshotName(seq)
	if err := zfs.SnapshotCreate(ctx, dataset, newSnap); err != nil {
		return "", "", fmt.Errorf("store: plan replication %s: create snapshot: %w", dataset, err)
	}
	return newSnap, baseSnap, nil
}

// SendResult holds the outcome of a replication send pipeline.
type SendResult struct {
	JobID    string
	BytesSent int64
}

// SendReplication executes `zfs send [-i base] dataset@snap | ssh destHost zfs recv dataset`
// using a 1 MB copy buffer. It blocks until the pipeline completes.
// destPort is the SSH port; pass 22 for the default.
func SendReplication(ctx context.Context, jobID, dataset, snapshot, baseSnapshot, destHost string, destPort int) (*SendResult, error) {
	sendArgs := []string{"send"}
	if baseSnapshot != "" {
		sendArgs = append(sendArgs, "-i", dataset+"@"+baseSnapshot)
	}
	sendArgs = append(sendArgs, dataset+"@"+snapshot)

	recvArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "BatchMode=yes",
		"-p", strconv.Itoa(destPort),
		destHost,
		"zfs", "recv", "-F", dataset,
	}

	sendCmd := exec.CommandContext(ctx, "zfs", sendArgs...)
	recvCmd := exec.CommandContext(ctx, "ssh", recvArgs...)

	sendOut, err := sendCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("store: repl %s: send stdout pipe: %w", jobID, err)
	}
	recvIn, err := recvCmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("store: repl %s: recv stdin pipe: %w", jobID, err)
	}

	var sendErr, recvErr error
	if sendErr = sendCmd.Start(); sendErr != nil {
		return nil, fmt.Errorf("store: repl %s: start send: %w", jobID, sendErr)
	}
	if recvErr = recvCmd.Start(); recvErr != nil {
		_ = sendCmd.Process.Kill()
		return nil, fmt.Errorf("store: repl %s: start recv: %w", jobID, recvErr)
	}

	buf := make([]byte, replBufSize)
	n, copyErr := io.CopyBuffer(recvIn, sendOut, buf)
	recvIn.Close()

	sendErr = sendCmd.Wait()
	recvErr = recvCmd.Wait()

	if copyErr != nil {
		return nil, fmt.Errorf("store: repl %s: copy: %w", jobID, copyErr)
	}
	if sendErr != nil {
		return nil, fmt.Errorf("store: repl %s: zfs send: %w", jobID, sendErr)
	}
	if recvErr != nil {
		return nil, fmt.Errorf("store: repl %s: zfs recv: %w", jobID, recvErr)
	}

	return &SendResult{JobID: jobID, BytesSent: n}, nil
}
