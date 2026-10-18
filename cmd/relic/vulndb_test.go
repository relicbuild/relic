package main

import (
	"testing"
	"time"
)

func TestDatabaseBuiltLineShowsTheLocalTime(t *testing.T) {
	t.Parallel()

	built := time.Date(2026, 10, 17, 6, 27, 9, 0, time.UTC)
	dubai := time.FixedZone("+04", 4*60*60)

	got := databaseBuiltLine(built, built.Add(9*time.Hour), dubai)
	if want := "built 2026-10-17 10:27:09 (9h0m0s ago)"; got != want {
		t.Errorf("databaseBuiltLine = %q, want %q", got, want)
	}
}
