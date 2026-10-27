package database

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"relic/internal/platform/localdisk"
)

func TestFullDiskMark(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		full bool
	}{
		{
			name: "PostgreSQL disk_full",
			err: &pgconn.PgError{
				Code: "53100",
			},
			full: true,
		},
		{
			name: "PostgreSQL insufficient_resources",
			err: &pgconn.PgError{
				Code: "53000",
			},
			full: true,
		},
		{
			name: "wrapped PostgreSQL disk_full",
			err: fmt.Errorf("insert: %w", &pgconn.PgError{
				Code: "53100",
			}),
			full: true,
		},
		{
			name: "PostgreSQL out_of_memory",
			err: &pgconn.PgError{
				Code: "53200",
			},
		},
		{
			name: "PostgreSQL unique violation",
			err: &pgconn.PgError{
				Code: "23505",
			},
		},
		{
			name: "other error",
			err:  errors.New("boom"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var full fullDisk

			got := full.mark(tc.err)

			if errors.Is(got, localdisk.ErrFull) != tc.full {
				t.Errorf("mark(%v) = %v, want localdisk.ErrFull: %v", tc.err, got, tc.full)
			}

			if !errors.Is(got, tc.err) {
				t.Errorf("mark(%v) = %v, lost the original error", tc.err, got)
			}

			if full.refusedSince(time.Now().Add(-time.Second)) != tc.full {
				t.Errorf("refusedSince after mark(%v) = %v, want %v", tc.err, !tc.full, tc.full)
			}
		})
	}
}

func TestFullDiskMarkKeepsNil(t *testing.T) {
	t.Parallel()

	var full fullDisk

	if err := full.mark(nil); err != nil {
		t.Errorf("mark(nil) = %v", err)
	}

	if full.refusedSince(time.Time{}) {
		t.Error("refusedSince is true without a refused write")
	}
}

func TestFullDiskRefusedSince(t *testing.T) {
	t.Parallel()

	var full fullDisk

	_ = full.mark(&pgconn.PgError{
		Code: "53100",
	})

	if !full.refusedSince(time.Now().Add(-time.Minute)) {
		t.Error("refusedSince a minute ago = false, want true")
	}

	if full.refusedSince(time.Now().Add(time.Minute)) {
		t.Error("refusedSince a minute from now = true, want false")
	}
}

var errDiskFull = &pgconn.PgError{
	Code: "53100",
}

type refusingPostgreSQL struct{}

func (refusingPostgreSQL) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errDiskFull
}

func (refusingPostgreSQL) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return refusingRows{}, nil
}

func (refusingPostgreSQL) QueryRow(context.Context, string, ...any) pgx.Row {
	return refusingRow{}
}

type refusingRows struct {
	pgx.Rows
}

func (refusingRows) Err() error { return errDiskFull }

type refusingRow struct{}

func (refusingRow) Scan(...any) error { return errDiskFull }

func TestFullMarkingPostgreSQL(t *testing.T) {
	t.Parallel()

	full := &fullDisk{}
	db := fullMarkingPostgreSQL{
		db:   refusingPostgreSQL{},
		full: full,
	}

	_, execErr := db.Exec(t.Context(), "INSERT")

	rows, err := db.Query(t.Context(), "INSERT RETURNING")
	if err != nil {
		t.Fatal(err)
	}

	for name, err := range map[string]error{
		"Exec":          execErr,
		"Query":         rows.Err(),
		"QueryRow.Scan": db.QueryRow(t.Context(), "INSERT RETURNING").Scan(),
	} {
		if !errors.Is(err, localdisk.ErrFull) {
			t.Errorf("%s = %v, want localdisk.ErrFull", name, err)
		}
	}

	if !full.refusedSince(time.Now().Add(-time.Second)) {
		t.Error("the refused writes were not recorded")
	}
}
