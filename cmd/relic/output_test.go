package main

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"

	"relic/internal/platform/terminal"
)

func tableRows(output string) [][]string {
	var rows [][]string

	for line := range strings.Lines(output) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "│") {
			continue
		}

		cells := strings.Split(strings.Trim(line, "│"), "│")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}

		rows = append(rows, cells)
	}

	return rows
}

func hasTableRow(output string, cells ...string) bool {
	return slices.ContainsFunc(tableRows(output), func(row []string) bool {
		return slices.Equal(row, cells)
	})
}

func hasTableRowWith(output string, cells ...string) bool {
	return slices.ContainsFunc(tableRows(output), func(row []string) bool {
		return !slices.ContainsFunc(cells, func(cell string) bool {
			return !slices.Contains(row, cell)
		})
	})
}

func TestPrintCompleted(t *testing.T) {
	t.Parallel()

	failure := errors.New("the third one failed")
	completed := terminal.Table{
		Headers: []string{
			"ITEM",
			"RESULT",
		},
		Rows: [][]string{
			{
				"libs-local/a",
				"deleted",
			},
		},
	}

	tests := []struct {
		name      string
		completed terminal.Table
		failure   error
		wantRow   bool
	}{
		{
			name:      "what was done, then the failure",
			completed: completed,
			failure:   failure,
			wantRow:   true,
		},
		{
			name:      "what was done",
			completed: completed,
			wantRow:   true,
		},
		{
			name: "nothing when nothing was done",
			completed: terminal.Table{
				Headers: completed.Headers,
			},
			failure: failure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			err := printCompleted(&out, tt.completed, tt.failure)
			if !errors.Is(err, tt.failure) || (tt.failure == nil) != (err == nil) {
				t.Errorf("error = %v, want %v", err, tt.failure)
			}

			if got := hasTableRow(
				out.String(),
				"libs-local/a",
				"deleted",
			); got != tt.wantRow ||
				(!tt.wantRow && out.Len() > 0) {
				t.Errorf("printed %q, want the row: %v", out.String(), tt.wantRow)
			}
		})
	}
}
