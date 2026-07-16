package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/floatlab/floatlab-core/pkg/store"
)

var (
	errBadZFSName = errors.New("invalid zfs name")
	errNotFound   = errors.New("zfs resource not found")
	errConflict   = errors.New("zfs resource conflict")
)

type CommandZFS struct{}

func (CommandZFS) DatasetExists(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "zfs", "list", "-H", "-o", "name", name)
	out, err := cmd.Output()
	if err == nil {
		return strings.TrimSpace(string(out)) == name, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, err
}

func (z CommandZFS) CreateDataset(ctx context.Context, name string, req store.CreateDatasetRequest) error {
	if err := validateDataset(name); err != nil {
		return err
	}
	for _, value := range []string{req.Quota, req.Compression, req.Recordsize} {
		if err := validatePropertyValue(value); err != nil {
			return err
		}
	}
	args := []string{"create"}
	for _, prop := range datasetProperties(req) {
		args = append(args, "-o", prop)
	}
	args = append(args, name)
	return z.run(ctx, args...)
}

func (z CommandZFS) ListDatasets(ctx context.Context) ([]store.Dataset, error) {
	out, err := z.output(ctx, "list", "-H", "-p", "-t", "filesystem,volume", "-o", "name,type,mountpoint,used,available,referenced,usedbysnapshots,quota")
	if err != nil {
		return nil, err
	}
	return parseDatasets(out)
}

func (z CommandZFS) GetDataset(ctx context.Context, name string) (store.Dataset, error) {
	if err := validateDataset(name); err != nil {
		return store.Dataset{}, err
	}
	out, err := z.output(ctx, "list", "-H", "-p", "-t", "filesystem,volume", "-o", "name,type,mountpoint,used,available,referenced,usedbysnapshots,quota", name)
	if err != nil {
		return store.Dataset{}, err
	}
	datasets, err := parseDatasets(out)
	if err != nil {
		return store.Dataset{}, err
	}
	if len(datasets) == 0 {
		return store.Dataset{}, errNotFound
	}
	return datasets[0], nil
}

func (z CommandZFS) DeleteDataset(ctx context.Context, name string, recursive bool) error {
	if err := validateDataset(name); err != nil {
		return err
	}
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, name)
	return z.run(ctx, args...)
}

func (z CommandZFS) ListSnapshots(ctx context.Context, dataset string, recursive bool) ([]store.Snapshot, error) {
	if err := validateDataset(dataset); err != nil {
		return nil, err
	}
	args := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,used,referenced", "-s", "creation"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, dataset)
	out, err := z.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseSnapshots(out)
}

func (z CommandZFS) CreateSnapshot(ctx context.Context, dataset, snapshot string, recursive bool) error {
	if err := validateDataset(dataset); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	args := []string{"snapshot"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, dataset+"@"+snapshot)
	return z.run(ctx, args...)
}

func (z CommandZFS) DeleteSnapshot(ctx context.Context, dataset, snapshot string, recursive bool) error {
	if err := validateDataset(dataset); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	args := []string{"destroy"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, dataset+"@"+snapshot)
	return z.run(ctx, args...)
}

func (z CommandZFS) SendSnapshot(ctx context.Context, dataset, snapshot, from string, recursive bool, dst io.Writer) error {
	if err := validateDataset(dataset); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	args := []string{"send"}
	if recursive {
		args = append(args, "-R")
	}
	if from != "" {
		if err := validateSnapshot(from); err != nil {
			return err
		}
		args = append(args, "-i", dataset+"@"+from)
	}
	args = append(args, dataset+"@"+snapshot)

	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdout = dst
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Wait(); err != nil {
		return mapZFSError(err, stderr.String())
	}
	return nil
}

func (z CommandZFS) ReceiveSnapshot(ctx context.Context, dataset string, forceRollback bool, src io.Reader) error {
	if err := validateDataset(dataset); err != nil {
		return err
	}
	args := []string{"receive"}
	if forceRollback {
		args = append(args, "-F")
	}
	args = append(args, dataset)
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdin = src
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return mapZFSError(err, stderr.String())
	}
	return nil
}

func (CommandZFS) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stdout, stderr limitedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", mapZFSError(err, stderr.String())
	}
	return stdout.String(), nil
}

func (CommandZFS) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stderr limitedBuffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return mapZFSError(err, stderr.String())
	}
	return nil
}

func datasetProperties(req store.CreateDatasetRequest) []string {
	var props []string
	if req.Quota != "" {
		props = append(props, "quota="+req.Quota)
	}
	if req.Compression != "" {
		props = append(props, "compression="+req.Compression)
	}
	if req.Recordsize != "" {
		props = append(props, "recordsize="+req.Recordsize)
	}
	return props
}

func parseDatasets(out string) ([]store.Dataset, error) {
	var datasets []store.Dataset
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 8 {
			return nil, fmt.Errorf("parse zfs dataset line: got %d fields", len(fields))
		}
		used, err := parseUintFields(fields[3], fields[4], fields[5], fields[6])
		if err != nil {
			return nil, err
		}
		ds := store.Dataset{
			Name:              fields[0],
			Type:              fields[1],
			Mountpoint:        fields[2],
			UsedBytes:         used[0],
			AvailableBytes:    used[1],
			ReferencedBytes:   used[2],
			SnapshotUsedBytes: used[3],
		}
		if fields[7] != "none" && fields[7] != "-" {
			quota, err := strconv.ParseUint(fields[7], 10, 64)
			if err != nil {
				return nil, err
			}
			ds.QuotaBytes = &quota
		}
		datasets = append(datasets, ds)
	}
	return datasets, nil
}

func parseSnapshots(out string) ([]store.Snapshot, error) {
	var snapshots []store.Snapshot
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			return nil, fmt.Errorf("parse zfs snapshot line: got %d fields", len(fields))
		}
		dataset, snapshot, err := splitSnapshotTarget(fields[0])
		if err != nil {
			return nil, err
		}
		values, err := parseUintFields(fields[1], fields[2], fields[3])
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, store.Snapshot{
			Name:            fields[0],
			Dataset:         dataset,
			Snapshot:        snapshot,
			CreatedAt:       time.Unix(int64(values[0]), 0).UTC(),
			UsedBytes:       values[1],
			ReferencedBytes: values[2],
		})
	}
	return snapshots, nil
}

func parseUintFields(fields ...string) ([]uint64, error) {
	out := make([]uint64, len(fields))
	for i, field := range fields {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return nil, err
		}
		out[i] = n
	}
	return out, nil
}

func validateDataset(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "@#") {
		return errBadZFSName
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") {
			return errBadZFSName
		}
		for _, r := range part {
			if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
				return errBadZFSName
			}
			if !(r == '_' || r == '-' || r == '.' || r == ':' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return errBadZFSName
			}
		}
	}
	return nil
}

func validateSnapshot(name string) error {
	if err := validateDataset(name); err != nil {
		return err
	}
	if strings.Contains(name, "/") {
		return errBadZFSName
	}
	return nil
}

func validatePropertyValue(value string) error {
	for _, r := range value {
		if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
			return errBadZFSName
		}
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r == '=' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return errBadZFSName
		}
	}
	return nil
}

func splitSnapshotTarget(target string) (string, string, error) {
	i := strings.LastIndex(target, "@")
	if i <= 0 || i == len(target)-1 {
		return "", "", errBadZFSName
	}
	dataset, snapshot := target[:i], target[i+1:]
	if err := validateDataset(dataset); err != nil {
		return "", "", err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return "", "", err
	}
	return dataset, snapshot, nil
}

func mapZFSError(err error, stderr string) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	wrapped := fmt.Errorf("%s: %w", msg, err)
	switch {
	case strings.Contains(msg, "dataset does not exist"),
		strings.Contains(msg, "snapshot does not exist"),
		strings.Contains(msg, "filesystem does not exist"):
		return fmt.Errorf("%w: %v", errNotFound, wrapped)
	case strings.Contains(msg, "dataset already exists"),
		strings.Contains(msg, "snapshot already exists"),
		strings.Contains(msg, "has children"),
		strings.Contains(msg, "dependent clones"),
		strings.Contains(msg, "destination has been modified"):
		return fmt.Errorf("%w: %v", errConflict, wrapped)
	default:
		return wrapped
	}
}

type limitedBuffer struct {
	bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const max = 64 << 10
	if b.Len() < max {
		_, _ = b.Buffer.Write(p[:min(len(p), max-b.Len())])
	}
	return len(p), nil
}
