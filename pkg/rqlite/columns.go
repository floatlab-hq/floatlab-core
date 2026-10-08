package rqlite

import (
	"context"
	"fmt"
	"strings"
)

// AddColumns applies trusted, compile-time additive schema changes once.
func AddColumns(ctx context.Context, db *Client, table string, definitions []string) error {
	result, err := db.Query(ctx, Statement{SQL: "PRAGMA table_info(" + table + ")"})
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, row := range result.Values {
		if len(row) > 1 {
			name, _ := row[1].(string)
			existing[name] = true
		}
	}
	for _, definition := range definitions {
		name := strings.Fields(definition)[0]
		if !existing[name] {
			if err := db.Execute(ctx, []Statement{{SQL: "ALTER TABLE " + table + " ADD COLUMN " + definition}}); err != nil {
				return fmt.Errorf("migrate %s.%s: %w", table, name, err)
			}
		}
	}
	return nil
}
