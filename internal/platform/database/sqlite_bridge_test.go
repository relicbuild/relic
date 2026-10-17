package database

import (
	"testing"
	"time"
	"uuid"
)

func TestSQLiteJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    any
		want string
	}{
		{
			name: "a nil slice is an empty array, not null",
			v:    []string(nil),
			want: "[]",
		},
		{
			name: "strings",
			v: []string{
				"a",
				"b",
			},
			want: `["a","b"]`,
		},
		{
			name: "uuids as text",
			v: []uuid.UUID{
				uuid.MustParse("0192f7a0-0000-7000-8000-000000000001"),
			},
			want: `["0192f7a0-0000-7000-8000-000000000001"]`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := sqliteJSON(tt.v); got != tt.want {
				t.Errorf("sqliteJSON(%#v) = %s, want %s", tt.v, got, tt.want)
			}
		})
	}
}

func TestSQLiteTime(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 10, 2, 9, 0, 0, 100_000_000, time.FixedZone("UTC+5", 5*3600))

	if got, want := sqliteTime(at), "2026-10-02 04:00:00.100000+00:00"; got != want {
		t.Errorf("sqliteTime = %s, want %s", got, want)
	}

	back, err := parseSQLiteTime(sqliteTime(at))
	if err != nil || !back.Equal(at) {
		t.Errorf("read back %v, %v; want %v", back, err, at)
	}

	if _, err := parseSQLiteTime("2026-10-02 05:00:00.123000+00:00"); err != nil {
		t.Error(err)
	}

	for _, bad := range []any{
		"",
		"not a date",
		"2026-13-45 99:99:99",
		[]byte("yesterday"),
		42,
	} {
		if got, err := parseSQLiteTime(bad); err == nil {
			t.Errorf("parseSQLiteTime(%#v) = %v; want an error", bad, got)
		}
	}
}
