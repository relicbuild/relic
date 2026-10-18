package main

import (
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

func completeTargets(g *globals) cobra.CompletionFunc {
	return func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		c, err := g.dial()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		repo, rest, found := strings.Cut(toComplete, "/")
		if !found {
			r, err := c.repositories.ListRepositories(
				cmd.Context(),
				connect.NewRequest(&relicv1.ListRepositoriesRequest{}),
			)
			if err != nil {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			var out []string

			for _, r := range r.Msg.GetRepositories() {
				out = append(out, r.GetName()+"/")
			}

			return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
		}

		dir := ""
		if i := strings.LastIndex(rest, "/"); i >= 0 {
			dir = rest[:i]
		}

		r, err := c.artifacts.ListChildren(
			cmd.Context(),
			connect.NewRequest(&relicv1.ListChildrenRequest{
				Repository: repo,
				Path:       dir,
			}),
		)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}

		var out []string

		for _, it := range r.Msg.GetItems() {
			s := repo + "/" + it.GetPath()
			if it.GetFolder() {
				s += "/"
			}

			out = append(out, s)
		}

		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	}
}

// completeTargetArg completes the argument at position from the server and
// leaves the others, local files, to the shell.
func completeTargetArg(g *globals, position int) cobra.CompletionFunc {
	targets := completeTargets(g)

	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) != position {
			return nil, cobra.ShellCompDirectiveDefault
		}

		return targets(cmd, args, toComplete)
	}
}

type transfer struct {
	fromRepo, from, toRepo, to string
}

func transfers(args []string) ([]transfer, error) {
	dst := args[len(args)-1]
	srcs := args[:len(args)-1]
	into := strings.HasSuffix(dst, "/") || len(srcs) > 1

	toRepo, toDir, _ := strings.Cut(strings.Trim(dst, "/"), "/")
	if toRepo == "" {
		return nil, fmt.Errorf("%q is not <repository>/<path>", dst)
	}

	if !into && toDir == "" {
		return nil, fmt.Errorf("%q names no path: end it with / to keep the source's name", dst)
	}

	var out []transfer

	for _, s := range srcs {
		repo, p, err := splitTarget(s)
		if err != nil {
			return nil, err
		}

		to := toDir
		if into {
			to = strings.TrimPrefix(path.Join(toDir, path.Base(p)), "/")
		}

		out = append(out, transfer{
			fromRepo: repo,
			from:     p,
			toRepo:   toRepo,
			to:       to,
		})
	}

	return out, nil
}

func copyMoveCmd(g *globals, move bool) *cobra.Command {
	var dryRun bool

	use, short, verb := "cp", "Copy files and folders", "copied"
	if move {
		use, short, verb = "mv", "Move files and folders", "moved"
	}

	cmd := &cobra.Command{
		Use:   use + " <repository>/<path>... <repository>/<path>",
		Short: short,
		Long: short + " within Relic, between repositories or inside one; folders go with everything below them.\n" +
			"A destination ending in / is a folder the sources keep their names in; with several sources it always is.",
		Example: fmt.Sprintf(
			"  relic artifact %[1]s libs-local/org/app/1.0/app.jar libs-release/org/app/1.0/app.jar\n"+
				"  relic artifact %[1]s libs-local/org/app/1.0/ libs-release/org/app/ --dry-run",
			use,
		),
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			ts, err := transfers(args)
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			done := terminal.Table{
				Headers: []string{
					"FROM",
					"TO",
					"RESULT",
				},
			}

			result := verb
			if dryRun {
				result += " (dry run)"
			}

			for _, t := range ts {
				g.progress.describe(use + " " + t.fromRepo + "/" + t.from)

				if move {
					_, err = c.artifacts.MoveItem(
						cmd.Context(),
						connect.NewRequest(&relicv1.MoveItemRequest{
							Repository:   t.fromRepo,
							Path:         t.from,
							ToRepository: t.toRepo,
							ToPath:       t.to,
							DryRun:       dryRun,
						}),
					)
				} else {
					_, err = c.artifacts.CopyItem(
						cmd.Context(),
						connect.NewRequest(&relicv1.CopyItemRequest{
							Repository:   t.fromRepo,
							Path:         t.from,
							ToRepository: t.toRepo,
							ToPath:       t.to,
							DryRun:       dryRun,
						}),
					)
				}

				if err != nil {
					return printCompleted(
						cmd.OutOrStdout(),
						done,
						fmt.Errorf("%s %s/%s to %s/%s: %w", use, t.fromRepo, t.from, t.toRepo, t.to, err),
					)
				}

				done.Rows = append(done.Rows, []string{
					t.fromRepo + "/" + t.from,
					t.toRepo + "/" + t.to,
					result,
				})
			}

			return printCompleted(cmd.OutOrStdout(), done, nil)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "check that it would work without doing it")

	return cmd
}

func removeCmd(g *globals) *cobra.Command {
	var ignoreNotFound bool

	cmd := &cobra.Command{
		Use:               "rm <repository>/<path>...",
		Short:             "Delete files and folders",
		Long:              "Deletes files and folders (with everything below them). Deleted items go to the trash, where an administrator can restore them.",
		Example:           "  relic artifact rm libs-local/org/app/1.0/ libs-local/org/app/1.1/app.jar",
		Args:              cobra.MinimumNArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			done := terminal.Table{
				Headers: []string{
					"ITEM",
					"RESULT",
				},
			}

			for _, a := range args {
				repo, p, err := splitTarget(a)
				if err != nil {
					return printCompleted(cmd.OutOrStdout(), done, err)
				}

				g.progress.describe("rm " + a)

				_, err = c.artifacts.DeleteItem(
					cmd.Context(),
					connect.NewRequest(&relicv1.DeleteItemRequest{
						Repository: repo,
						Path:       p,
					}),
				)
				if isNotFound(err) && ignoreNotFound {
					continue
				}

				if err != nil {
					return printCompleted(cmd.OutOrStdout(), done, fmt.Errorf("rm %s/%s: %w", repo, p, err))
				}

				done.Rows = append(done.Rows, []string{
					repo + "/" + p,
					"deleted",
				})
			}

			return printCompleted(cmd.OutOrStdout(), done, nil)
		},
	}
	cmd.Flags().BoolVar(&ignoreNotFound, "ignore-not-found", false, "treat a missing item as deleted")

	return cmd
}

func propsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "props",
		Short: "Show and change the properties of files and folders",
	}
	cmd.AddCommand(
		propsGetCmd(g),
		propsSetCmd(g),
		propsDeleteCmd(g),
	)

	return cmd
}

func propsGetCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:               "get <repository>/<path>",
		Short:             "Show the properties of a file or folder",
		Example:           "  relic artifact props get libs-local/org/app/1.0/app.jar -o json",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.artifacts.GetProperties(
				cmd.Context(),
				connect.NewRequest(&relicv1.GetPropertiesRequest{
					Repository: repo,
					Path:       p,
				}),
			)
			if err != nil {
				return fmt.Errorf("props get %s/%s: %w", repo, p, err)
			}

			props := r.Msg.GetProperties()

			return printMessage(cmd.OutOrStdout(), output, r.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"KEY",
						"VALUES",
					},
				}

				for _, k := range slices.Sorted(func(yield func(string) bool) {
					for k := range props {
						if !yield(k) {
							return
						}
					}
				}) {
					table.Rows = append(table.Rows, []string{
						k,
						strings.Join(props[k].GetValues(), ","),
					})
				}

				return table.Print(w)
			})
		},
	}
	outputFlag(cmd, &output)

	return cmd
}

func parseProperties(args []string) (map[string]*relicv1.PropertyValues, error) {
	out := map[string]*relicv1.PropertyValues{}

	for _, a := range args {
		k, v, ok := strings.Cut(a, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("%q is not key=value", a)
		}

		out[k] = &relicv1.PropertyValues{
			Values: strings.Split(v, ","),
		}
	}

	return out, nil
}

func propsSetCmd(g *globals) *cobra.Command {
	var recursive bool

	cmd := &cobra.Command{
		Use:               "set <repository>/<path> <key>=<value>[,<value>]...",
		Short:             "Set properties on a file or folder",
		Long:              "Sets properties, replacing the values of the keys given; other properties stay.",
		Example:           "  relic artifact props set libs-local/org/app/1.0/ release=1.0 qa=passed,signed --recursive",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			props, err := parseProperties(args[1:])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.artifacts.SetProperties(
				cmd.Context(),
				connect.NewRequest(&relicv1.SetPropertiesRequest{
					Repository: repo,
					Path:       p,
					Properties: props,
					Recursive:  recursive,
				}),
			); err != nil {
				return fmt.Errorf("props set %s/%s: %w", repo, p, err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s properties set\n", repo, p)

			return nil
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "also set them on everything below a folder")

	return cmd
}

func propsDeleteCmd(g *globals) *cobra.Command {
	var recursive bool

	cmd := &cobra.Command{
		Use:               "delete <repository>/<path> <key>...",
		Short:             "Delete properties from a file or folder",
		Example:           "  relic artifact props delete libs-local/org/app/1.0/ qa --recursive",
		Args:              cobra.MinimumNArgs(2),
		ValidArgsFunction: completeTargets(g),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			if _, err := c.artifacts.DeleteProperties(
				cmd.Context(),
				connect.NewRequest(&relicv1.DeletePropertiesRequest{
					Repository: repo,
					Path:       p,
					Keys:       args[1:],
					Recursive:  recursive,
				}),
			); err != nil {
				return fmt.Errorf("props delete %s/%s: %w", repo, p, err)
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s properties deleted\n", repo, p)

			return nil
		},
	}
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "also delete them from everything below a folder")

	return cmd
}
