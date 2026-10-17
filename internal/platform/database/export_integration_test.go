//go:build integration

package database

import (
	"context"
	"encoding/json"
	"testing"
)

// CanonicalRows reads every table Copy copies in the engines' common form.
func CanonicalRows(ctx context.Context, t *testing.T, db, kinds *DB) map[string][]string {
	t.Helper()

	tables, err := db.copyTables(ctx)
	if err != nil {
		t.Fatal(err)
	}

	out := map[string][]string{}

	for _, table := range tables {
		cols, err := kinds.columns(ctx, table)
		if err != nil {
			t.Fatal(err)
		}

		rows := []string{}

		if _, err := db.readRows(ctx, table, cols, func(batch string) error {
			var list []json.RawMessage
			if err := json.Unmarshal([]byte(batch), &list); err != nil {
				return err
			}

			for _, r := range list {
				rows = append(rows, string(r))
			}

			return nil
		}); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}

		out[table] = rows
	}

	return out
}
