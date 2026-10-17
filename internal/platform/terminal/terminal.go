// Package terminal renders what Relic's commands print for people: its
// colors, tables and sizes.
package terminal

import (
	"fmt"
	"io"
	"slices"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
)

// Primary is Relic's primary color, the one its web UI uses.
var Primary = lipgloss.Color("#22c55e")

// Edge is the color of borders.
var Edge = lipgloss.Color("#475569")

// Table is rows under headers.
type Table struct {
	Headers      []string
	RightAligned []int
	Rows         [][]string
}

// Print writes the table to w, with colors when w is a terminal.
func (t Table) Print(w io.Writer) error {
	rendered := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Foreground(Edge)).
		Headers(t.Headers...).
		Rows(t.Rows...).
		StyleFunc(func(row, column int) lipgloss.Style {
			style := lipgloss.NewStyle().Padding(0, 1)
			if slices.Contains(t.RightAligned, column) {
				style = style.Align(lipgloss.Right)
			}

			if row == table.HeaderRow {
				return style.Bold(true).Foreground(Primary)
			}

			return style
		})

	if _, err := lipgloss.Fprintln(w, rendered); err != nil {
		return fmt.Errorf("printing a table: %w", err)
	}

	return nil
}

// ByteSize is a number of bytes in the largest binary unit that keeps it
// at one or more.
func ByteSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}

	divisor, exponent := int64(unit), 0
	for value := bytes / unit; value >= unit; value /= unit {
		divisor *= unit
		exponent++
	}

	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(divisor), "KMGTPE"[exponent])
}
