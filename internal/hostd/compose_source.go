package hostd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/floatlab/floatlab-core/internal/update"
	"github.com/floatlab/floatlab-core/pkg/compose"
	_ "modernc.org/sqlite"
)

const stackStateFile = "state.sqlite"

func publishComposeSource(ctx context.Context, path, stackID string, content []byte) error {
	stateDir := filepath.Join(filepath.Dir(path), compose.ProjectStateDir)
	if err := os.Mkdir(stateDir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(stateDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("stack state requires a private directory: %s", stateDir)
	}
	statePath := filepath.Join(stateDir, stackStateFile)
	file, err := os.OpenFile(statePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		if err := file.Close(); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err = os.Lstat(statePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("stack state requires a private regular file: %s", statePath)
	}
	dsn := url.URL{Scheme: "file", Path: statePath, RawQuery: "mode=rw&_txlock=immediate&_pragma=busy_timeout(10000)&_pragma=trusted_schema(OFF)"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS compose_source (
		singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
		stack_id TEXT NOT NULL,
		sha256 TEXT NOT NULL
	)`); err != nil {
		return err
	}
	mode := os.FileMode(0644)
	var current []byte
	info, err = os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("compose source is not a regular file")
		}
		mode = info.Mode().Perm()
		current, err = os.ReadFile(path)
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A retry after publication can commit the checkpoint without rewriting the file.
	if info == nil || !bytes.Equal(current, content) {
		if err := update.ReplaceFile(path, content, mode); err != nil {
			return err
		}
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	if _, err := tx.ExecContext(ctx, `INSERT INTO compose_source(singleton,stack_id,sha256) VALUES(1,?,?)
		ON CONFLICT(singleton) DO UPDATE SET stack_id=excluded.stack_id,sha256=excluded.sha256
		WHERE stack_id<>excluded.stack_id OR sha256<>excluded.sha256`, stackID, digest); err != nil {
		return err
	}
	return tx.Commit()
}
