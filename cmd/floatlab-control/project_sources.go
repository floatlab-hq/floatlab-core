package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/compose"
	"github.com/floatlab/floatlab-core/pkg/config"
	"github.com/floatlab/floatlab-core/pkg/hostclient"
	"github.com/floatlab/floatlab-core/pkg/ipc"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
)

func projectStackStore(local, labDB *rqlite.Client, hosts *hostclient.Pool) *config.Store {
	return config.NewProjectStore(local, labDB, readProjectCompose, func(ctx context.Context, stack *config.Stack) error {
		_, err := hosts.Execute(ctx, stack.PrimaryNodeID, "compose.source.write", ipc.ComposeSourcePayload{
			StackID: stack.ID, DatasetPath: stack.ZFSDataset, ComposeFile: stack.ComposeYAML,
		})
		return err
	})
}

func readProjectCompose(_ context.Context, stack *config.Stack) (string, error) {
	path, err := compose.ProjectSourcePath(stack.ZFSDataset)
	if err != nil {
		return "", err
	}
	return compose.ReadProjectSource(path)
}

func projectReplicaStatements(ctx context.Context, snapshot *rqlite.Client) ([]rqlite.Statement, error) {
	view := config.NewProjectStore(snapshot, nil, readProjectCompose, nil)
	stacks, err := view.ListStackCatalog(ctx)
	if err != nil {
		return nil, err
	}
	var writes []rqlite.Statement
	for _, stack := range stacks {
		if stack.ComposeFile {
			content, err := readProjectCompose(ctx, stack)
			if err != nil {
				return nil, fmt.Errorf("export Compose for %s: %w", stack.Name, err)
			}
			writes = append(writes, rqlite.Statement{
				SQL:    `UPDATE stacks SET compose_yaml=?, compose_file=0 WHERE id=?`,
				Params: []any{content, stack.ID},
			})
		}
	}
	return writes, nil
}

func migrateProjectSources(ctx context.Context, stacks *config.Store, nodeID string) error {
	pending, err := stacks.PendingStackCompose(ctx)
	if err != nil {
		return err
	}
	var issues []error
	for _, stack := range pending {
		if stack.PrimaryNodeID != nodeID {
			issues = append(issues, fmt.Errorf("stack %s belongs to node %s", stack.ID, stack.PrimaryNodeID))
			continue
		}
		if err := migrateProjectSource(ctx, stacks, stack); err != nil {
			issues = append(issues, fmt.Errorf("stack %s: %w", stack.Name, err))
		}
	}
	return errors.Join(issues...)
}

func migrateProjectSource(ctx context.Context, stacks *config.Store, stack *config.Stack) error {
	path, err := compose.ProjectSourcePath(stack.ZFSDataset)
	if err != nil {
		return err
	}
	current, err := compose.ReadProjectSource(path)
	if errors.Is(err, os.ErrNotExist) {
		mounted, err := projectDatasetMounted(filepath.Dir(path), stack.ZFSDataset)
		if err != nil || !mounted {
			// A queued provision, move, or restore can own the first publication.
			return err
		}
		return stacks.PublishStackCompose(ctx, stack)
	}
	if err != nil {
		return err
	}
	if current != stack.ComposeYAML {
		dir := filepath.Join(filepath.Dir(path), compose.ProjectStateDir)
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return fmt.Errorf("preserving the previous Compose requires a private directory: %s", dir)
		}
		name := fmt.Sprintf("compose-before-cutover-%x.yml", sha256.Sum256([]byte(current)))
		if err := update.ReplaceFile(filepath.Join(dir, name), []byte(current), 0600); err != nil {
			return fmt.Errorf("preserve previous Compose: %w", err)
		}
	}
	return stacks.PublishStackCompose(ctx, stack)
}

func projectDatasetMounted(mountpoint, dataset string) (bool, error) {
	mounts, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for line := range strings.SplitSeq(string(mounts), "\n") {
		mount, filesystem, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		fields, source := strings.Fields(mount), strings.Fields(filesystem)
		if len(fields) >= 5 && len(source) >= 2 && fields[4] == mountpoint && source[0] == "zfs" && source[1] == dataset {
			return true, nil
		}
	}
	return false, nil
}
