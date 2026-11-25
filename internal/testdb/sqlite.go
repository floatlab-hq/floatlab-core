// Package testdb serves SQLite over the rqlite HTTP protocol for tests.
package testdb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/floatlab/floatlab-core/pkg/rqlite"
	_ "modernc.org/sqlite"
)

func Start(t *testing.T) *rqlite.Client {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var statements [][]any
		if err := json.NewDecoder(r.Body).Decode(&statements); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for _, statement := range statements {
			if len(statement) == 0 {
				http.Error(w, "empty SQL statement", http.StatusBadRequest)
				return
			}
			if _, ok := statement[0].(string); !ok {
				http.Error(w, "expected SQL string", http.StatusBadRequest)
				return
			}
		}
		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()
		results := make([]map[string]any, 0, len(statements))
		for _, statement := range statements {
			var result map[string]any
			result, err = execute(tx, r.URL.Path, statement[0].(string), statement[1:])
			if err != nil {
				results = append(results, map[string]any{"error": err.Error()})
				break
			}
			results = append(results, result)
		}
		if err == nil {
			if err := tx.Commit(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(server.Close)
	return rqlite.NewClient(server.URL)
}

var dateColumn = regexp.MustCompile(`(?i)(^|[(,\n]|\bADD[ \t]+COLUMN\b)([ \t]*[a-z_][a-z_0-9]*[ \t]+)(DATETIME|TIMESTAMP|DATE)\b`)

func execute(tx *sql.Tx, path, statement string, args []any) (map[string]any, error) {
	if path == "/db/execute" {
		// Keep numeric affinity while returning dates in their original SQLite representation.
		ddl := strings.ToUpper(strings.TrimSpace(statement))
		if strings.HasPrefix(ddl, "CREATE TABLE ") || strings.HasPrefix(ddl, "ALTER TABLE ") {
			statement = dateColumn.ReplaceAllString(statement, "${1}${2}${3}_RAW")
		}
		result, err := tx.Exec(statement, args...)
		if err != nil {
			return nil, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		id, err := result.LastInsertId()
		return map[string]any{"rows_affected": affected, "last_insert_id": id}, err
	}
	rows, err := tx.Query(statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	values := make([][]any, 0)
	for rows.Next() {
		row := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range row {
			dest[i] = &row[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := map[string]any{"columns": columns, "values": values}
	if path == "/db/request" && len(columns) == 0 {
		var changes int64
		if err := tx.QueryRow("SELECT changes()").Scan(&changes); err != nil {
			return nil, fmt.Errorf("rows affected: %w", err)
		}
		result["rows_affected"] = changes
	}
	return result, nil
}
