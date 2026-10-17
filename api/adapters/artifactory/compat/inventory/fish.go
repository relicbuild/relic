package main

import (
	"regexp"
	"slices"
	"strings"
)

var fishCommand = regexp.MustCompile(`(?m)^complete (?:-r )?-c jf -n '([^']*)' -a '([^']+)'`)

func fishParents(script string) map[string]bool {
	parents := map[string]bool{}

	for _, m := range fishCommand.FindAllStringSubmatch(script, -1) {
		if cond, ok := strings.CutPrefix(m[1], "__fish_seen_subcommand_from "); ok {
			for name := range strings.FieldsSeq(cond) {
				parents[name] = true
			}
		}
	}

	return parents
}

func fishNameGroups(script string) [][]string {
	matches := fishCommand.FindAllStringSubmatch(script, -1)

	groups := make([][]string, 0, len(matches))
	for _, m := range matches {
		groups = append(groups, strings.Fields(m[2]))
	}

	return groups
}

func crossCheck(visible []discovered, fish [][]string) []string {
	key := func(names []string) string {
		return strings.Join(names, " ")
	}
	fromHelp := map[string]bool{}

	for _, d := range visible {
		fields := strings.Fields(d.Path)
		fromHelp[key(append([]string{
			fields[len(fields)-1],
		}, d.Aliases...))] = true
	}

	fromFish := map[string]bool{}
	for _, g := range fish {
		fromFish[key(g)] = true
	}

	var diffs []string

	for k := range fromHelp {
		if k != "help h" && !fromFish[k] {
			diffs = append(diffs, "only in --help: "+k)
		}
	}

	for k := range fromFish {
		if k != "help h" && !fromHelp[k] {
			diffs = append(diffs, "only in fish completion: "+k)
		}
	}

	slices.Sort(diffs)

	return diffs
}
