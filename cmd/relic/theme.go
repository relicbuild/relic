package main

import (
	"fmt"
	"io"
	"strings"

	"charm.land/lipgloss/v2"

	"relic/internal/platform/terminal"
)

var (
	colorGreen = terminal.Primary
	colorAmber = lipgloss.Color("#f59e0b")
	colorRed   = lipgloss.Color("#ef4444")
	colorCyan  = lipgloss.Color("#06b6d4")
	colorText  = lipgloss.Color("#e2e8f0")
	colorLabel = lipgloss.Color("#94a3b8")
	colorMuted = lipgloss.Color("#64748b")
	colorEdge  = terminal.Edge
)

var (
	styleTitle = lipgloss.NewStyle().Bold(true).Foreground(colorGreen)
	styleLabel = lipgloss.NewStyle().Foreground(colorLabel)
	styleValue = lipgloss.NewStyle().Foreground(colorText)
	styleMuted = lipgloss.NewStyle().Foreground(colorMuted)
	styleGood  = lipgloss.NewStyle().Bold(true).Foreground(colorGreen)
	styleWarn  = lipgloss.NewStyle().Foreground(colorAmber)
	styleBad   = lipgloss.NewStyle().Bold(true).Foreground(colorRed)
	styleRun   = lipgloss.NewStyle().Foreground(colorCyan)
	stylePanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorEdge).
			Padding(0, 1)
)

type panelRow struct {
	label string
	value string
}

func panel(title, subtitle string, rows []panelRow) string {
	width := 0
	for _, r := range rows {
		width = max(width, len(r.label))
	}

	lines := make([]string, 0, len(rows)+2)

	head := styleTitle.Render(title)
	if subtitle != "" {
		head += "  " + styleMuted.Render(subtitle)
	}

	lines = append(lines, head, "")

	for _, r := range rows {
		lines = append(lines, styleLabel.Render(fmt.Sprintf("%-*s", width+2, r.label))+r.value)
	}

	return stylePanel.Render(strings.Join(lines, "\n"))
}

func printStyled(w io.Writer, s string) {
	_, _ = lipgloss.Fprintln(w, s)
}

func stateStyle(state string) lipgloss.Style {
	switch state {
	case "create":
		return styleGood
	case "update":
		return styleWarn
	case "remove":
		return styleBad
	case "run":
		return styleRun
	default:
		return styleMuted
	}
}

func check(ok bool, good, bad string) string {
	if ok {
		return styleGood.Render("✓ ") + styleValue.Render(good)
	}

	return styleBad.Render("✗ " + bad)
}
