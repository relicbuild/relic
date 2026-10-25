package main

import (
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	relicv1 "relic/api/gen/relic/v1"
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

func TestDatabaseStatusText(t *testing.T) {
	t.Parallel()

	built := time.Date(2026, 10, 17, 6, 27, 9, 0, time.UTC)
	failed := time.Date(2026, 10, 18, 8, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		status *relicv1.ScannerStatus
		want   string
	}{
		{
			name:   "scanning off",
			status: &relicv1.ScannerStatus{},
			want:   scanOffLine + "\nno scan has used a vulnerability database yet\n",
		},
		{
			name: "a database",
			status: &relicv1.ScannerStatus{
				Available:                  true,
				VulnerabilityDatabaseBuilt: timestamppb.New(built),
			},
			want: "built 2026-10-17 06:27:09 (2h0m0s ago)\n",
		},
		{
			name: "a failed import",
			status: &relicv1.ScannerStatus{
				Available:                         true,
				VulnerabilityDatabaseImportError:  "import vulnerability database: grype: exit status 1: unexpected EOF",
				VulnerabilityDatabaseImportFailed: timestamppb.New(failed),
			},
			want: "no scan has used a vulnerability database yet\n" +
				"the latest import failed at 2026-10-18 08:00:00: " +
				"import vulnerability database: grype: exit status 1: unexpected EOF\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := databaseStatusText(tt.status, built.Add(2*time.Hour), time.UTC); got != tt.want {
				t.Errorf("databaseStatusText = %q, want %q", got, tt.want)
			}
		})
	}
}
