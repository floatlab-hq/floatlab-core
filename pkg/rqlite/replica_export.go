package rqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
)

// ExportReplica enriches an isolated, consistent peer view without writing to the live database.
func (c *Client) ExportReplica(ctx context.Context, directory string, enrich func(context.Context, *Client) ([]Statement, error)) (string, func(), error) {
	if c.local == nil {
		return "", nil, errors.New("replica export requires a local database")
	}
	staging, err := os.MkdirTemp(directory, ".outgoing-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	path := filepath.Join(staging, "replica.sqlite")
	if err := c.populateReplica(ctx, path, enrich); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

func (c *Client) populateReplica(ctx context.Context, path string, enrich func(context.Context, *Client) ([]Statement, error)) error {
	if _, err := c.local.db.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	dsn := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw"}
	db, err := sql.Open("sqlite", dsn.String())
	if err != nil {
		return err
	}
	defer db.Close()
	snapshot := &Client{local: &localDB{db: db, path: path}}
	statements, err := enrich(ctx, snapshot)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt.SQL, stmt.Params...); err != nil {
			return err
		}
	}
	// Existing peers can consume this projection with the original replica protocol.
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=1`); err != nil {
		return err
	}
	return tx.Commit()
}
