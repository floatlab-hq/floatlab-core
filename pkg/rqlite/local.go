package rqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

type LocalIdentity struct {
	NodeID     string `json:"node_id"`
	Generation string `json:"generation"`
	Revision   int64  `json:"revision"`
}

const LocalSchemaVersion = 1

type localDB struct {
	db      *sql.DB
	path    string
	changes chan struct{}
}

// OpenLocal opens the sole writable workload database for this node.
func OpenLocal(path, nodeID string) (*Client, error) {
	if nodeID == "" {
		return nil, errors.New("workload database requires node identity")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0600)
	f.Close()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+url.PathEscape(path)+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	c := &Client{local: &localDB{db: db, path: path, changes: make(chan struct{}, 1)}}
	fail := func(err error) (*Client, error) { db.Close(); return nil, err }
	var schema int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&schema); err != nil {
		return fail(err)
	}
	if schema > LocalSchemaVersion {
		return fail(fmt.Errorf("workload schema %d requires newer software", schema))
	}
	if _, err = db.Exec(fmt.Sprintf(`PRAGMA user_version=%d`, LocalSchemaVersion)); err != nil {
		return fail(err)
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS local_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL); CREATE TABLE IF NOT EXISTS workload_identity (singleton INTEGER PRIMARY KEY CHECK(singleton=1), node_id TEXT NOT NULL, generation TEXT NOT NULL, revision INTEGER NOT NULL); INSERT OR IGNORE INTO workload_identity VALUES (1, ?, ?, 0)`, nodeID, uuid.NewString()); err != nil {
		return fail(err)
	}
	id, err := c.LocalIdentity(context.Background())
	if err != nil {
		return fail(err)
	}
	if id.NodeID != nodeID {
		return fail(fmt.Errorf("workload database belongs to node %s", id.NodeID))
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fail(err)
	}
	return c, nil
}

func (c *Client) Close() error {
	if c.local != nil {
		return c.local.db.Close()
	}
	return nil
}
func (c *Client) LocalPath() string {
	if c.local == nil {
		return ""
	}
	return c.local.path
}
func (c *Client) Changes() <-chan struct{} {
	if c.local == nil {
		return nil
	}
	return c.local.changes
}
func (c *Client) LocalIdentity(ctx context.Context) (LocalIdentity, error) {
	var id LocalIdentity
	if c.local == nil {
		return id, errors.New("not a local database")
	}
	err := c.local.db.QueryRowContext(ctx, `SELECT node_id,generation,revision FROM workload_identity WHERE singleton=1`).Scan(&id.NodeID, &id.Generation, &id.Revision)
	return id, err
}
func (l *localDB) notify() {
	select {
	case l.changes <- struct{}{}:
	default:
	}
}
func (l *localDB) execute(ctx context.Context, stmts []Statement) error {
	if len(stmts) == 0 {
		return nil
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, s := range stmts {
		if _, err = tx.ExecContext(ctx, s.SQL, s.Params...); err != nil {
			return fmt.Errorf("local sqlite execute: %w", err)
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workload_identity SET revision=revision+1 WHERE singleton=1`); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	l.notify()
	return nil
}
func (l *localDB) query(ctx context.Context, s Statement, write bool) (*QueryResult, error) {
	if !write {
		rows, err := l.db.QueryContext(ctx, s.SQL, s.Params...)
		if err != nil {
			return nil, err
		}
		return collectRows(rows)
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, s.SQL, s.Params...)
	if err != nil {
		return nil, err
	}
	result, err := collectRows(rows)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT changes()`).Scan(&result.RowsAffected); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE workload_identity SET revision=revision+1 WHERE singleton=1`); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	l.notify()
	return result, nil
}
func collectRows(rows *sql.Rows) (*QueryResult, error) {
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	result := &QueryResult{Columns: cols}
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}
	for _, t := range types {
		result.Types = append(result.Types, strings.ToLower(t.DatabaseTypeName()))
	}
	for rows.Next() {
		values := make([]interface{}, len(cols))
		dest := make([]interface{}, len(cols))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return nil, err
		}
		// Preserve the HTTP client's JSON representation for existing callers.
		for i, v := range values {
			switch n := v.(type) {
			case int64:
				values[i] = float64(n)
			case []byte:
				values[i] = string(n)
			case time.Time:
				values[i] = n.Format(time.RFC3339Nano)
			}
		}
		result.Values = append(result.Values, values)
	}
	return result, rows.Err()
}

// ImportLocal copies explicitly selected owner-scoped tables into an empty local database.
func ImportLocal(ctx context.Context, source, target *Client, tables []string) error {
	if target.local == nil {
		return errors.New("import target must be local")
	}
	done, err := target.LocalMigrationDone(ctx, "legacy-rqlite")
	if err != nil || done {
		return err
	}
	data, err := readTables(ctx, source, tables)
	if err != nil {
		return err
	}
	var stmts []Statement
	for index, table := range tables {
		if !validSQLName(table) {
			return fmt.Errorf("invalid table name %q", table)
		}
		rows := data[index]
		count, err := target.Query(ctx, Statement{SQL: `SELECT count(*) FROM "` + table + `"`})
		if err != nil {
			return err
		}
		if len(count.Values) > 0 && count.Values[0][0] != float64(0) {
			return fmt.Errorf("import target table %s is not empty", table)
		}
		for _, values := range rows.Values {
			quoted := make([]string, len(rows.Columns))
			marks := make([]string, len(rows.Columns))
			for i, col := range rows.Columns {
				if !validSQLName(col) {
					return errors.New("invalid column name")
				}
				quoted[i] = `"` + col + `"`
				marks[i] = "?"
			}
			stmts = append(stmts, Statement{SQL: `INSERT INTO "` + table + `" (` + strings.Join(quoted, ",") + `) VALUES (` + strings.Join(marks, ",") + `)`, Params: values})
		}
	}
	stmts = append(stmts, Statement{SQL: `INSERT INTO local_migrations(name,applied_at) VALUES('legacy-rqlite',?)`, Params: []any{time.Now().UTC().Format(time.RFC3339Nano)}})
	return target.Execute(ctx, stmts)
}

func (c *Client) LocalMigrationDone(ctx context.Context, name string) (bool, error) {
	if c.local == nil {
		return false, errors.New("migration target must be local")
	}
	rows, err := c.Query(ctx, Statement{SQL: `SELECT name FROM local_migrations WHERE name=?`, Params: []any{name}})
	if err != nil {
		return false, err
	}
	return len(rows.Values) > 0, nil
}

func readTables(ctx context.Context, c *Client, tables []string) ([]*QueryResult, error) {
	queries := make([]Statement, len(tables))
	for i, table := range tables {
		if !validSQLName(table) {
			return nil, fmt.Errorf("invalid table name %q", table)
		}
		queries[i] = Statement{SQL: `SELECT * FROM "` + table + `"`}
	}
	return c.QueryBatch(ctx, queries)
}

// CopyTables atomically replaces a committed cache without changing local workload
// revision. The cache tables must not contain authoritative workload state.
func CopyTables(ctx context.Context, source, target *Client, tables []string) error {
	if target.local == nil {
		return errors.New("cache target must be local")
	}
	var stmts []Statement
	preserveTOTP := false
	data, err := readTables(ctx, source, tables)
	if err != nil {
		return err
	}
	for index, table := range tables {
		if !validSQLName(table) {
			return fmt.Errorf("invalid table name %q", table)
		}
		rows := data[index]
		if table == "user_credentials" {
			preserveTOTP = true
		}
		stmts = append(stmts, Statement{SQL: `DELETE FROM "` + table + `"`})
		for _, values := range rows.Values {
			cols := make([]string, len(rows.Columns))
			marks := make([]string, len(rows.Columns))
			for i, col := range rows.Columns {
				if !validSQLName(col) {
					return errors.New("invalid cache column")
				}
				cols[i] = `"` + col + `"`
				marks[i] = "?"
			}
			stmts = append(stmts, Statement{SQL: `INSERT INTO "` + table + `" (` + strings.Join(cols, ",") + `) VALUES (` + strings.Join(marks, ",") + `)`, Params: values})
		}
	}
	tx, err := target.local.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var totpCounters [][]interface{}
	if preserveTOTP {
		rows, e := tx.QueryContext(ctx, `SELECT username,last_totp_step,totp_secret FROM user_credentials`)
		if e != nil {
			return e
		}
		prior, e := collectRows(rows)
		if e != nil {
			return e
		}
		totpCounters = prior.Values
	}
	for _, s := range stmts {
		if _, err = tx.ExecContext(ctx, s.SQL, s.Params...); err != nil {
			return err
		}
	}
	for _, row := range totpCounters {
		if _, err = tx.ExecContext(ctx, `UPDATE user_credentials SET last_totp_step=MAX(last_totp_step,?) WHERE username=? AND totp_secret=?`, row[1], row[0], row[2]); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func validSQLName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r != '_' && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
