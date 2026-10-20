package database

import "testing"

func TestReadOnlyStatement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		want  bool
	}{
		{
			name:  "a select after the query's name",
			query: "-- name: GetItem :one\nSELECT id FROM items WHERE id = @id",
			want:  true,
		},
		{
			name:  "a select starting its own line",
			query: "-- name: ListItems :many\n-- One line of explanation.\nSELECT\nid FROM items",
			want:  true,
		},
		{
			name:  "a read-only common table expression",
			query: "WITH recent AS (SELECT id FROM items) SELECT id FROM recent",
			want:  true,
		},
		{
			name:  "an insert returning a row",
			query: "-- name: CreateRepository :one\nINSERT INTO repositories (key) VALUES (@key) RETURNING id",
			want:  false,
		},
		{
			name:  "a common table expression that deletes",
			query: "WITH gone AS (SELECT id FROM items) DELETE FROM items WHERE id IN (SELECT id FROM gone)",
			want:  false,
		},
		{
			name:  "an update",
			query: "UPDATE items SET size = 1",
			want:  false,
		},
		{
			name:  "a pragma",
			query: "PRAGMA wal_checkpoint(TRUNCATE)",
			want:  false,
		},
		{
			name:  "nothing but comments",
			query: "-- name: Nothing :exec",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := readOnlyStatement(tt.query); got != tt.want {
				t.Errorf("readOnlyStatement(%q) = %v, want %v", tt.query, got, tt.want)
			}
		})
	}
}
