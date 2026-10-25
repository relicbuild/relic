package main

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pmezard/go-difflib/difflib"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"relic/internal/platform/terminal"
)

func isNotFound(err error) bool { return connect.CodeOf(err) == connect.CodeNotFound }

func applyCmd(g *globals) *cobra.Command {
	var (
		files []string
		force bool
	)

	cmd := &cobra.Command{
		Use:   "apply -f <file>...",
		Short: "Create or update the resources in YAML files",
		Long: "Creates each relic.build/v1 resource that does not exist and updates the ones that differ, in file order.\n" +
			"A resource's spec owns the top-level fields it names; fields it leaves out keep their current values.\n" +
			"Write-only fields (passwords) are not compared: change one with --force.",
		Example: "  relic apply -f repositories.yaml -f access.yaml\n  relic get repository libs -o yaml | relic apply -f -",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			docs, err := readDocuments(files, cmd.InOrStdin())
			if err != nil {
				return err
			}

			ctx, out := cmd.Context(), cmd.OutOrStdout()
			applied := terminal.Table{
				Headers: []string{
					"RESOURCE",
					"RESULT",
				},
			}

			for _, d := range docs {
				g.progress.describe("Applying " + d.String())

				result := "configured"

				live, err := d.kind.get(ctx, c, d.name)
				switch {
				case isNotFound(err):
					if err := d.kind.create(ctx, c, d.msg); err != nil {
						return printCompleted(out, applied, fmt.Errorf("create %s: %w", d, err))
					}

					result = "created"
				case err != nil:
					return printCompleted(out, applied, fmt.Errorf("get %s: %w", d, err))
				case !force &&
					proto.Equal(restrict(live, d.owned, d.kind), restrict(d.msg, d.owned, d.kind)):
					result = "unchanged"
				default:
					if err := d.kind.update(ctx, c, merged(live, d)); err != nil {
						return printCompleted(out, applied, fmt.Errorf("update %s: %w", d, err))
					}
				}

				applied.Rows = append(applied.Rows, []string{
					d.String(),
					result,
				})
			}

			return printCompleted(out, applied, nil)
		},
	}
	cmd.Flags().
		StringSliceVarP(&files, "filename", "f", nil, "YAML file with relic.build/v1 resources; - reads standard input")
	cmd.Flags().BoolVar(&force, "force", false, "update resources even when they look unchanged (to set a password)")

	return cmd
}

func diffCmd(g *globals) *cobra.Command {
	var (
		files    []string
		exitCode bool
	)

	cmd := &cobra.Command{
		Use:   "diff -f <file>...",
		Short: "Show what apply would change",
		Long:  "Prints a unified diff, per resource, of the fields its spec owns: the server's values against the file's.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			docs, err := readDocuments(files, cmd.InOrStdin())
			if err != nil {
				return err
			}

			changed := false

			for _, d := range docs {
				live, err := d.kind.get(cmd.Context(), c, d.name)
				if err != nil && !isNotFound(err) {
					return fmt.Errorf("get %s: %w", d, err)
				}

				var from proto.Message
				if err == nil {
					from = restrict(live, d.owned, d.kind)
				}

				text, err := unifiedDiff(d, from, restrict(d.msg, d.owned, d.kind))
				if err != nil {
					return err
				}

				if text != "" {
					changed = true
					_, _ = fmt.Fprint(cmd.OutOrStdout(), text)
				}
			}

			if changed && exitCode {
				return exitCode1
			}

			return nil
		},
	}
	cmd.Flags().
		StringSliceVarP(&files, "filename", "f", nil, "YAML file with relic.build/v1 resources; - reads standard input")
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "exit with status 1 when there are differences")

	return cmd
}

const exitCode1 = exitCode(1)

func unifiedDiff(d document, live, want proto.Message) (string, error) {
	a := ""

	if live != nil {
		b, err := toYAML(d.kind, live)
		if err != nil {
			return "", err
		}

		a = string(b)
	}

	b, err := toYAML(d.kind, want)
	if err != nil {
		return "", err
	}

	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(a),
		B:        difflib.SplitLines(string(b)),
		FromFile: d.String() + " (server)",
		ToFile:   d.String() + " (file)",
		Context:  3,
	})
}

func deleteCmd(g *globals) *cobra.Command {
	var (
		files          []string
		ignoreNotFound bool
	)

	cmd := &cobra.Command{
		Use:   "delete -f <file>...",
		Short: "Delete the resources in YAML files",
		Long:  "Deletes each resource named in the files, in reverse order, so resources are removed before what they refer to.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			docs, err := readDocuments(files, cmd.InOrStdin())
			if err != nil {
				return err
			}

			deleted := terminal.Table{
				Headers: []string{
					"RESOURCE",
					"RESULT",
				},
			}

			for _, d := range slices.Backward(docs) {
				g.progress.describe("Deleting " + d.String())

				err := d.kind.delete(cmd.Context(), c, d.name)
				switch {
				case isNotFound(err) && ignoreNotFound:
					continue
				case err != nil:
					return printCompleted(cmd.OutOrStdout(), deleted, fmt.Errorf("delete %s: %w", d, err))
				}

				deleted.Rows = append(deleted.Rows, []string{
					d.String(),
					"deleted",
				})
			}

			return printCompleted(cmd.OutOrStdout(), deleted, nil)
		},
	}
	cmd.Flags().
		StringSliceVarP(&files, "filename", "f", nil, "YAML file with relic.build/v1 resources; - reads standard input")
	cmd.Flags().BoolVar(&ignoreNotFound, "ignore-not-found", false, "do not fail for resources that do not exist")

	return cmd
}

func getCmd(g *globals) *cobra.Command {
	var output string

	names := make([]string, 0, len(kinds))
	for _, k := range kinds {
		names = append(names, k.aliases[0])
	}

	cmd := &cobra.Command{
		Use:   "get <kind> [name]",
		Short: "List resources, or show one",
		Long: "Kinds: " + strings.Join(
			names,
			", ",
		) + ".\nWith -o yaml the output is relic.build/v1 documents that relic apply accepts.",
		Example:           "  relic get repositories\n  relic get repository libs-local -o yaml",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: completeResources(g, names),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := kindNamed(args[0])
			if err != nil {
				return err
			}

			if !slices.Contains([]string{
				"text",
				"name",
				"yaml",
				"json",
			}, output) {
				return fmt.Errorf("unknown output %q: use text, name, yaml or json", output)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			var items []proto.Message
			if len(args) == 2 {
				m, err := k.get(cmd.Context(), c, args[1])
				if err != nil {
					return fmt.Errorf("get %s/%s: %w", strings.ToLower(k.name), args[1], err)
				}

				items = []proto.Message{
					m,
				}
			} else if items, err = k.list(cmd.Context(), c); err != nil {
				return fmt.Errorf("list %s: %w", k.aliases[1], err)
			}

			return printResources(cmd.OutOrStdout(), k, items, output, len(args) == 1)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "text", "output format: text, name, yaml or json")

	return cmd
}

func completeResources(g *globals, names []string) cobra.CompletionFunc {
	return func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		switch len(args) {
		case 0:
			return names, cobra.ShellCompDirectiveNoFileComp
		case 1:
			return g.completeNamesAt(1, resourceNames(args[0]), nil)(cmd, args, toComplete)
		default:
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
	}
}

func printResources(w io.Writer, k *kind, items []proto.Message, output string, list bool) error {
	if output == "json" {
		return printResourcesJSON(w, items, list)
	}

	if output == "text" {
		table := terminal.Table{
			Headers: []string{
				"NAME",
			},
		}

		for _, m := range items {
			table.Rows = append(table.Rows, []string{
				nameOf(m, k.nameField),
			})
		}

		return table.Print(w)
	}

	for i, m := range items {
		switch output {
		case "name":
			_, _ = fmt.Fprintln(w, nameOf(m, k.nameField))
		case "yaml":
			b, err := toYAML(k, m)
			if err != nil {
				return err
			}

			if i > 0 {
				_, _ = fmt.Fprintln(w, "---")
			}

			if _, err := w.Write(b); err != nil {
				return err
			}
		}
	}

	return nil
}

func printResourcesJSON(w io.Writer, items []proto.Message, list bool) error {
	docs := make([]json.RawMessage, 0, len(items))

	for _, m := range items {
		b, err := protojson.Marshal(m)
		if err != nil {
			return err
		}

		docs = append(docs, b)
	}

	var (
		out []byte
		err error
	)

	if list {
		out, err = json.MarshalIndent(docs, "", "  ")
	} else if len(docs) == 1 {
		out, err = json.MarshalIndent(docs[0], "", "  ")
	}

	if err != nil {
		return err
	}

	_, err = fmt.Fprintln(w, string(out))

	return err
}
