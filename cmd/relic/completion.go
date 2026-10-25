package main

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"
)

type nameSource func(context.Context, *client) ([]string, error)

type argumentCompletion struct {
	path     string
	position int
	names    nameSource
}

type flagCompletion struct {
	flag  string
	names nameSource
}

func resourceNames(alias string) nameSource {
	return func(ctx context.Context, c *client) ([]string, error) {
		k, err := kindNamed(alias)
		if err != nil {
			return nil, err
		}

		items, err := k.list(ctx, c)
		if err != nil {
			return nil, err
		}

		names := make([]string, 0, len(items))
		for _, m := range items {
			names = append(names, nameOf(m, k.nameField))
		}

		return names, nil
	}
}

func releaseNames(ctx context.Context, c *client) ([]string, error) {
	r, err := c.releases.ListReleases(ctx, &relicv1.ListReleasesRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(r.GetReleases()))
	for _, release := range r.GetReleases() {
		names = append(names, release.GetName())
	}

	return names, nil
}

func buildNames(ctx context.Context, c *client) ([]string, error) {
	r, err := c.builds.ListBuilds(ctx, &relicv1.ListBuildsRequest{})
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(r.GetBuilds()))
	for _, build := range r.GetBuilds() {
		names = append(names, build.GetName())
	}

	return names, nil
}

func argumentCompletions() []argumentCompletion {
	repositories := resourceNames("repository")
	webhooks := resourceNames("webhook")
	cleanupPolicies := resourceNames("cleanuppolicy")

	completions := make([]argumentCompletion, 0, 22)
	completions = append(completions, []argumentCompletion{
		{
			path:     "registry list",
			position: 0,
			names:    repositories,
		},
		{
			path:     "replication run",
			position: 0,
			names:    repositories,
		},
		{
			path:     "image promote",
			position: 1,
			names:    repositories,
		},
		{
			path:     "package huggingface upload",
			position: 1,
			names:    repositories,
		},
		{
			path:     "package huggingface download",
			position: 0,
			names:    repositories,
		},
		{
			path:     "package publish terraform",
			position: 0,
			names:    repositories,
		},
		{
			path:     "cleanup preview",
			position: 0,
			names:    cleanupPolicies,
		},
		{
			path:     "cleanup run",
			position: 0,
			names:    cleanupPolicies,
		},
		{
			path:     "webhook ping",
			position: 0,
			names:    webhooks,
		},
		{
			path:     "webhook deliveries",
			position: 0,
			names:    webhooks,
		},
	}...)

	for _, verb := range []string{
		"get",
		"list",
		"promote",
		"finalize",
		"tag",
		"export",
		"delete",
	} {
		completions = append(completions, argumentCompletion{
			path:     "release " + verb,
			position: 0,
			names:    releaseNames,
		})
	}

	for _, verb := range []string{
		"get",
		"list",
		"scan",
		"promote",
		"discard",
	} {
		completions = append(completions, argumentCompletion{
			path:     "build " + verb,
			position: 0,
			names:    buildNames,
		})
	}

	return completions
}

func flagCompletions() []flagCompletion {
	return []flagCompletion{
		{
			flag:  "repository",
			names: resourceNames("repository"),
		},
		{
			flag:  "watch",
			names: resourceNames("watch"),
		},
	}
}

func (g *globals) completeNames(root *cobra.Command) {
	for _, a := range argumentCompletions() {
		cmd, _, err := root.Find(strings.Fields(a.path))
		if err != nil || cmd.CommandPath() != "relic "+a.path {
			continue
		}

		cmd.ValidArgsFunction = g.completeNamesAt(a.position, a.names, cmd.ValidArgsFunction)
	}

	var walk func(*cobra.Command)

	walk = func(cmd *cobra.Command) {
		for _, f := range flagCompletions() {
			if cmd.Flags().Lookup(f.flag) == nil || namesAnotherServer(cmd) {
				continue
			}

			if _, ok := cmd.GetFlagCompletionFunc(f.flag); ok {
				continue
			}

			_ = cmd.RegisterFlagCompletionFunc(f.flag, g.completeNamesAt(-1, f.names, nil))
		}

		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func namesAnotherServer(cmd *cobra.Command) bool {
	return strings.HasPrefix(cmd.CommandPath(), "relic import ")
}

func (g *globals) completeNamesAt(position int, names nameSource, next cobra.CompletionFunc) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if position >= 0 && len(args) != position {
			if next != nil {
				return next(cmd, args, toComplete)
			}

			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		c, err := g.dial()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		out, err := names(cmd.Context(), c)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		return out, cobra.ShellCompDirectiveNoFileComp
	}
}
