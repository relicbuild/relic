package main

import (
	"context"
	"slices"
	"strings"
)

type helpFunc func(ctx context.Context, args ...string) string

type discovered struct {
	Path     string
	Aliases  []string
	Category string
	Hidden   bool
	Flags    []string
}

func walk(ctx context.Context, help helpFunc, parents map[string]bool) []discovered {
	var (
		out   []discovered
		visit func(parent []string, text string)
	)

	visit = func(parent []string, text string) {
		for _, e := range parseHelp(text).Subcommands {
			path := append(slices.Clone(parent), e.Name)
			d, nsText := describe(ctx, help, path, parents)
			d.Path, d.Aliases, d.Category = strings.Join(path, " "), e.Aliases, e.Category
			out = append(out, d)

			if nsText != "" {
				visit(path, nsText)
			}
		}
	}
	visit(nil, help(ctx, "--help"))

	return out
}

func probeHidden(ctx context.Context, help helpFunc, candidates []string, seen []discovered) []discovered {
	var out []discovered

	for _, c := range candidates {
		if slices.ContainsFunc(seen, func(d discovered) bool {
			return d.Path == c
		}) {
			continue
		}

		path := strings.Fields(c)
		if len(path) == 0 {
			continue
		}

		if d, _ := describe(ctx, help, path, nil); d.Path != "" {
			d.Hidden = true
			out = append(out, d)
		}
	}

	return out
}

func describe(ctx context.Context, help helpFunc, path []string, parents map[string]bool) (discovered, string) {
	joined := strings.Join(path, " ")
	parent, leaf := path[:len(path)-1], path[len(path)-1]
	text := help(ctx, append(slices.Clone(parent), "help", leaf)...)

	if !hasOptionsSection(text) || parents[leaf] {
		if ns := help(
			ctx,
			append(slices.Clone(path), "--help")...); isHelpFor(ns) &&
			len(parseHelp(ns).Subcommands) > 0 {
			return discovered{
				Path:  joined,
				Flags: parseHelp(ns).Flags,
			}, ns
		}
	}

	if !isHelpFor(text) {
		return discovered{}, ""
	}

	return discovered{
		Path:  joined,
		Flags: parseHelp(text).Flags,
	}, ""
}

func hasOptionsSection(text string) bool {
	for line := range strings.Lines(text) {
		switch strings.TrimSpace(line) {
		case "Options:", "OPTIONS:":
			return true
		}
	}

	return false
}
