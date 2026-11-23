package config

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/compose-spec/compose-go/v2/template"
	"github.com/floatlab/floatlab-core/pkg/rqlite"
	"gopkg.in/yaml.v3"
)

func (s *Store) loadStackCompose(ctx context.Context, stack *Stack) error {
	if !stack.ComposeFile {
		return nil
	}
	source, err := s.readStackCompose(ctx, stack)
	if err != nil {
		return err
	}
	stack.ComposeYAML = source
	return stack.readComposePolicy()
}

// ReadStackCompose returns the source as recorded, including invalid edits an operator may need to repair.
func (s *Store) ReadStackCompose(ctx context.Context, id string) (string, error) {
	stack, err := s.CatalogStack(ctx, id)
	if err != nil {
		return "", err
	}
	return s.readStackCompose(ctx, stack)
}

func (s *Store) readStackCompose(ctx context.Context, stack *Stack) (string, error) {
	if !stack.ComposeFile {
		return stack.ComposeYAML, nil
	}
	source, err := s.readCompose(ctx, stack)
	if err != nil {
		return "", fmt.Errorf("config: read Compose for %s: %w", stack.ZFSDataset, err)
	}
	return source, nil
}

// PublishStackCompose installs the current draft into its mounted project before discarding it from the catalog.
func (s *Store) PublishStackCompose(ctx context.Context, stack *Stack) error {
	if !s.ProjectComposeEnabled() {
		return errors.New("config: project Compose is disabled")
	}
	s.composeMu.Lock()
	defer s.composeMu.Unlock()
	current, err := s.CatalogStack(ctx, stack.ID)
	if err != nil {
		return err
	}
	if current.Name != stack.Name || current.ZFSDataset != stack.ZFSDataset || current.PrimaryNodeID != stack.PrimaryNodeID || current.BackupNodeID != stack.BackupNodeID {
		return fmt.Errorf("config: stack placement changed before Compose publication: %s", stack.ID)
	}
	if !current.ComposeFile && current.ComposeYAML != stack.ComposeYAML {
		return fmt.Errorf("config: Compose draft changed before publication: %s", stack.ID)
	}
	stack.UpdatedAt = current.UpdatedAt
	return s.publishStackCompose(ctx, stack, current.ComposeFile)
}

func (s *Store) publishStackCompose(ctx context.Context, stack *Stack, reconcileTargets bool) error {
	if err := stack.readComposePolicy(); err != nil {
		return err
	}
	if err := s.writeCompose(ctx, stack); err != nil {
		return fmt.Errorf("config: publish Compose for %s: %w", stack.ZFSDataset, err)
	}
	if reconcileTargets {
		stack.UpdatedAt = time.Now().UTC()
	}
	statements := []rqlite.Statement{{
		SQL: `UPDATE stacks SET compose_file=1, compose_yaml='', updated_at=?
		      WHERE id=? AND primary_node_id=? AND zfs_dataset=?`,
		Params: []interface{}{stack.UpdatedAt, stack.ID, stack.PrimaryNodeID, stack.ZFSDataset},
	}}
	if s.db.LocalPath() != "" {
		// Older managers cannot use a catalog whose Compose drafts have been discarded.
		statements = append(statements, rqlite.Statement{SQL: fmt.Sprintf("PRAGMA user_version=%d", rqlite.ProjectLocalSchemaVersion)})
	}
	if err := s.db.Execute(ctx, statements); err != nil {
		return fmt.Errorf("config: record Compose publication: %w", err)
	}
	current, err := s.CatalogStack(ctx, stack.ID)
	if err != nil {
		return err
	}
	if !current.ComposeFile || current.PrimaryNodeID != stack.PrimaryNodeID || current.ZFSDataset != stack.ZFSDataset {
		return fmt.Errorf("config: stack changed during Compose publication: %s", stack.ID)
	}
	stack.ComposeFile = true
	if reconcileTargets {
		if err := s.SyncBackupTargets(ctx, stack); err != nil {
			return err
		}
	}
	s.notify(ChangeEvent{Entity: "stack", Action: "update", ID: stack.ID})
	return nil
}

func (stack *Stack) readComposePolicy() error {
	var project struct {
		Name      string `yaml:"name"`
		Placement *struct {
			Primary  string `yaml:"primary_node"`
			Backup   string `yaml:"secondary_node"`
			Failover struct {
				Mode             string `yaml:"mode"`
				AutoTriggerAfter string `yaml:"auto_trigger_after"`
			} `yaml:"failover"`
		} `yaml:"x-fl-stack"`
	}
	if stack.ComposeYAML == "" {
		return fmt.Errorf("config: empty Compose for %s", stack.ZFSDataset)
	}
	if err := yaml.Unmarshal([]byte(stack.ComposeYAML), &project); err != nil {
		return fmt.Errorf("config: invalid Compose for %s: %w", stack.ZFSDataset, err)
	}
	if project.Name != "" && project.Name != stack.Name {
		return fmt.Errorf("config: Compose name %q does not match stack name %q", project.Name, stack.Name)
	}
	if project.Placement == nil {
		stack.FailoverMode, stack.AutoTriggerAfter = "manual", ""
		return nil
	}
	placement := project.Placement
	// Match the project loader's interpolation environment for FloatLab metadata.
	for _, value := range []*string{&placement.Primary, &placement.Backup, &placement.Failover.Mode, &placement.Failover.AutoTriggerAfter} {
		resolved, err := template.Substitute(*value, func(string) (string, bool) { return "", false })
		if err != nil {
			return fmt.Errorf("config: interpolate Compose metadata for %s: %w", stack.ZFSDataset, err)
		}
		*value = resolved
	}
	if placement.Primary != "" && placement.Primary != stack.PrimaryNodeID || placement.Backup != "" && placement.Backup != stack.BackupNodeID {
		return fmt.Errorf("config: Compose cannot change stack placement for %s", stack.Name)
	}
	stack.FailoverMode, stack.AutoTriggerAfter = placement.Failover.Mode, placement.Failover.AutoTriggerAfter
	if stack.FailoverMode == "" {
		stack.FailoverMode = "manual"
	}
	if stack.FailoverMode != "manual" && stack.FailoverMode != "auto" {
		return fmt.Errorf("config: invalid Compose failover mode %q for %s", stack.FailoverMode, stack.Name)
	}
	if stack.AutoTriggerAfter == "" {
		stack.AutoTriggerAfter = "120s"
	}
	if delay, err := time.ParseDuration(stack.AutoTriggerAfter); err != nil || delay <= 0 {
		return fmt.Errorf("config: invalid Compose failover delay %q for %s", stack.AutoTriggerAfter, stack.Name)
	}
	return nil
}
