package scan

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relic/internal/platform/scripttest"
)

func TestNewScannerSlots(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		budget    int64
		wantCount int
		wantEach  int64
	}{
		{
			name:      "a budget below one scanner's memory still runs one",
			budget:    100 << 20,
			wantCount: 1,
			wantEach:  100 << 20,
		},
		{
			name:      "a 256 MiB budget runs one scanner",
			budget:    256 << 20,
			wantCount: 1,
			wantEach:  256 << 20,
		},
		{
			name:      "a 1 GiB budget runs two scanners of 512 MiB",
			budget:    1 << 30,
			wantCount: 2,
			wantEach:  512 << 20,
		},
		{
			name:      "a large budget runs at most two",
			budget:    8 << 30,
			wantCount: 2,
			wantEach:  4 << 30,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			slots := NewScannerSlots(tt.budget)
			if slots.Count() != tt.wantCount || slots.ScannerMemory() != tt.wantEach {
				t.Errorf("slots = %d of %d bytes, want %d of %d", slots.Count(), slots.ScannerMemory(),
					tt.wantCount, tt.wantEach)
			}
		})
	}
}

func TestExecSetsTheScannerMemoryLimit(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "scanner")
	scripttest.Write(t, script, "#!/bin/sh\nenv\n")

	tests := []struct {
		name  string
		run   func(Exec) ([]byte, error)
		slots *ScannerSlots
		want  string
	}{
		{
			name: "a scanner gets its share",
			run: func(e Exec) ([]byte, error) {
				return e.run(t.Context())
			},
			slots: NewScannerSlots(1 << 30),
			want:  "GOMEMLIMIT=536870912",
		},
		{
			name: "a database change gets the whole budget",
			run: func(e Exec) ([]byte, error) {
				return e.runAlone(t.Context())
			},
			slots: NewScannerSlots(1 << 30),
			want:  "GOMEMLIMIT=1073741824",
		},
		{
			name: "no slots, no limit",
			run: func(e Exec) ([]byte, error) {
				return e.run(t.Context())
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out, err := tt.run(Exec{
				Path:  script,
				Slots: tt.slots,
			})
			if err != nil {
				t.Fatal(err)
			}

			var got string

			for line := range strings.Lines(string(out)) {
				if strings.HasPrefix(line, "GOMEMLIMIT=") {
					got = strings.TrimSpace(line)
				}
			}

			if got != tt.want {
				t.Errorf("environment holds %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDatabaseChangeWaitsForRunningScanners(t *testing.T) {
	t.Parallel()

	slots := NewScannerSlots(1 << 30)

	_, release, err := slots.acquire(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	if _, _, err := slots.acquire(ctx, true); err == nil {
		t.Fatal("a database change started while a scanner ran")
	}

	release()

	_, releaseChange, err := slots.acquire(t.Context(), true)
	if err != nil {
		t.Fatalf("database change after the scanner finished: %v", err)
	}

	scannerCtx, cancelScanner := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelScanner()

	if _, _, err := slots.acquire(scannerCtx, false); err == nil {
		t.Error("a scanner started during a database change")
	}

	feedCtx, cancelFeed := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelFeed()

	if err := slots.WithoutDatabaseChanges(feedCtx, func(context.Context) error {
		return nil
	}); err == nil {
		t.Error("a feed import ran during a database change")
	}

	releaseChange()

	if err := slots.WithoutDatabaseChanges(t.Context(), func(context.Context) error {
		return nil
	}); err != nil {
		t.Errorf("feed import after the database change: %v", err)
	}
}
