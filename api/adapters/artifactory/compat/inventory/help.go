package main

import (
	"bufio"
	"regexp"
	"strings"
)

type helpEntry struct {
	Name     string
	Aliases  []string
	Category string
}

type helpPage struct {
	Subcommands []helpEntry
	Flags       []string
}

var (
	commandLine  = regexp.MustCompile(`^( {1,8})([a-zA-Z0-9][a-zA-Z0-9-]*(?:, [a-zA-Z0-9][a-zA-Z0-9-]*)*)(?: {2,}\S|$)`)
	categoryLine = regexp.MustCompile(`^ {1,8}(\S[^:]*):\s*$`)
	flagLine     = regexp.MustCompile(`^ {1,8}--([a-zA-Z0-9][a-zA-Z0-9-]*)`)
)

func parseHelp(text string) helpPage {
	var page helpPage

	section, category := "", ""
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)

	for sc.Scan() {
		line := sc.Text()
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			section, category = strings.TrimSuffix(strings.TrimSpace(line), ":"), ""
			continue
		}

		switch strings.ToUpper(section) {
		case "COMMANDS":
			if m := categoryLine.FindStringSubmatch(line); m != nil {
				category = m[1]
				continue
			}

			if m := commandLine.FindStringSubmatch(line); m != nil {
				names := strings.Split(m[2], ", ")
				if names[0] == "help" {
					continue
				}

				page.Subcommands = append(
					page.Subcommands,
					helpEntry{
						Name:     names[0],
						Aliases:  names[1:],
						Category: category,
					},
				)
			}
		case "OPTIONS", "GLOBAL OPTIONS":
			if m := flagLine.FindStringSubmatch(line); m != nil && m[1] != "help" {
				page.Flags = append(page.Flags, m[1])
			}
		}
	}

	return page
}

func isHelpFor(text string) bool {
	return (strings.Contains(text, "Name:") || strings.Contains(text, "NAME:")) &&
		!strings.Contains(text, "is not a jf command") && !strings.Contains(text, "No help topic")
}
