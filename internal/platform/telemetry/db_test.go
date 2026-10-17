package telemetry

import (
	"strings"
	"testing"
)

func TestQueryName(t *testing.T) {
	t.Parallel()

	for sql, want := range map[string]string{
		"-- name: GetItem :one\nSELECT 1":                      "GetItem",
		"\n  -- name: JobGetAvailable :many\nWITH ...":         "JobGetAvailable",
		"SELECT * FROM items WHERE name = 'x'":                 Other,
		"-- name: " + strings.Repeat("A", 65) + " :one":        Other,
		"-- name: " + strings.Repeat("A", 64) + " :one":        strings.Repeat("A", 64),
		"SELECT 1 -- name: Injected :one":                      Other,
		"-- name: Bad-Name :one":                               Other,
		"-- name: CountUsers :one\nSELECT count(*) FROM users": "CountUsers",
	} {
		if got := QueryName(sql); got != want {
			t.Errorf("QueryName(%q) = %q, want %q", sql, got, want)
		}
	}
}
