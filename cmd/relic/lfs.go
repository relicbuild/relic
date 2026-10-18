package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

const lfsPointerMax = 1024

var lfsOID = regexp.MustCompile(`(?m)^oid sha256:([0-9a-f]{64})$`)

func lfsCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lfs",
		Short: "Manage Git LFS objects stored in Relic",
	}
	cmd.AddCommand(lfsCleanCmd(g))

	return cmd
}

func lfsCleanCmd(g *globals) *cobra.Command {
	var (
		refs    []string
		gitDir  string
		dryRun  bool
		output  string
		verbose bool
	)

	cmd := &cobra.Command{
		Use:   "clean [<repository>]",
		Short: "Delete Git LFS objects no Git reference uses",
		Long: "Reads the LFS pointers in the trees of the Git references that match --refs, lists the objects " +
			"stored in the repository (named by their sha256 oid) and deletes the ones no pointer names. " +
			"Without <repository>, it is read from the lfs.url of .lfsconfig or of the Git config " +
			"(its last path segment). Deleted objects go to the trash.",
		Example: "  relic artifact lfs clean --dry-run\n  relic artifact lfs clean lfs-local --refs 'refs/heads/**,refs/tags/**' -o json",
		Args:    cobra.MaximumNArgs(1),
		ValidArgsFunction: func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 || strings.Contains(toComplete, "/") {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}

			out, dir := completeTargets(g)(cmd, args, toComplete)
			for i, s := range out {
				out[i] = strings.TrimSuffix(s, "/")
			}

			return out, dir &^ cobra.ShellCompDirectiveNoSpace
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			repo := ""
			if len(args) == 1 {
				repo = args[0]
			} else {
				r, err := lfsRepository(cmd.Context(), gitDir)
				if err != nil {
					return err
				}

				repo = r
			}

			keep, err := lfsReferencedObjects(cmd.Context(), gitDir, refs)
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			stored, err := c.files(cmd.Context(), repo, "")
			if err != nil {
				return fmt.Errorf("lfs clean: list %s: %w", repo, err)
			}

			result := &relicv1.SearchItemsResponse{}
			objects := 0

			for _, it := range stored {
				if !lfsName(it.GetName()) {
					continue
				}

				objects++

				if _, ok := keep[it.GetName()]; ok {
					continue
				}

				if !dryRun {
					if _, err := c.artifacts.DeleteItem(
						cmd.Context(),
						&relicv1.DeleteItemRequest{
							Repository: repo,
							Path:       it.GetPath(),
						},
					); err != nil && !isNotFound(err) {
						return fmt.Errorf("lfs clean: delete %s/%s: %w", repo, it.GetPath(), err)
					}
				}

				result.Items = append(result.Items, it)
			}

			return printMessage(cmd.OutOrStdout(), output, result, func(w io.Writer) error {
				verb := "deleted"
				if dryRun {
					verb = "would delete"
				}

				if verbose {
					table := terminal.Table{
						Headers: []string{
							"OBJECT",
							"SIZE",
							"RESULT",
						},
						RightAligned: []int{
							1,
						},
					}

					for _, object := range result.GetItems() {
						table.Rows = append(table.Rows, []string{
							repo + "/" + object.GetPath(),
							terminal.ByteSize(object.GetSize()),
							verb,
						})
					}

					if err := table.Print(w); err != nil {
						return err
					}
				}

				_, _ = fmt.Fprintf(
					w,
					"%s: %d objects referenced, %d stored, %s %d\n",
					repo,
					len(keep),
					objects,
					verb,
					len(result.GetItems()),
				)

				return nil
			})
		},
	}
	cmd.Flags().
		StringSliceVar(&refs, "refs", []string{
			"refs/remotes/**",
		}, "Git references whose objects to keep (for-each-ref patterns, comma-separated; ** crosses /)")
	cmd.Flags().StringVar(&gitDir, "git-dir", ".", "the Git working tree")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be deleted without deleting it")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "list every object deleted")
	outputFlag(cmd, &output)

	return cmd
}

func lfsName(name string) bool {
	return len(name) == 64 && strings.Trim(name, "0123456789abcdef") == ""
}

func lfsRepository(ctx context.Context, dir string) (string, error) {
	for _, q := range [][]string{
		{
			"config",
			"--file",
			filepath.Join(dir, ".lfsconfig"),
			"lfs.url",
		},
		{
			"-C",
			dir,
			"config",
			"lfs.url",
		},
		{
			"-C",
			dir,
			"config",
			"remote.origin.lfsurl",
		},
	} {
		out, err := git(ctx, q...)
		if err != nil {
			continue
		}

		u, err := url.Parse(strings.TrimSpace(string(out)))
		if err != nil || u.Path == "" {
			continue
		}

		if name := path.Base(path.Clean(u.Path)); name != "/" && name != "." {
			return name, nil
		}
	}

	return "", errors.New("lfs clean: no LFS URL in .lfsconfig or the Git config: name the repository")
}

func lfsReferencedObjects(ctx context.Context, dir string, patterns []string) (map[string]struct{}, error) {
	out, err := git(ctx, append([]string{
		"-C",
		dir,
		"for-each-ref",
		"--format=%(refname)",
	}, patterns...)...)
	if err != nil {
		return nil, fmt.Errorf("lfs clean: read references: %w", err)
	}

	blobs := map[string]struct{}{}

	for ref := range strings.Lines(string(out)) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}

		tree, err := git(ctx, "-C", dir, "ls-tree", "-r", "-l", "-z", ref+"^{tree}")
		if err != nil {
			return nil, fmt.Errorf("lfs clean: read %s: %w", ref, err)
		}

		for entry := range bytes.SplitSeq(tree, []byte{
			0,
		}) {
			meta, _, ok := bytes.Cut(entry, []byte{
				'\t',
			})
			f := strings.Fields(string(meta))

			if !ok || len(f) != 4 || f[1] != "blob" {
				continue
			}

			if size, err := strconv.Atoi(f[3]); err == nil && size <= lfsPointerMax {
				blobs[f[2]] = struct{}{}
			}
		}
	}

	keep := map[string]struct{}{}
	if len(blobs) == 0 {
		return keep, nil
	}

	var in bytes.Buffer
	for b := range blobs {
		in.WriteString(b + "\n")
	}

	var stderr bytes.Buffer

	//nolint:gosec // G204: git with a fixed subcommand; dir is an argument, never a shell
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	cmd.Stdin = &in
	cmd.Stderr = &stderr

	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("lfs clean: read pointers: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	r := bufio.NewReader(bytes.NewReader(raw))

	for {
		header, err := r.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		f := strings.Fields(header)
		if len(f) != 3 {
			continue
		}

		size, err := strconv.Atoi(f[2])
		if err != nil {
			return nil, fmt.Errorf("lfs clean: read pointers: %q", header)
		}

		body := make([]byte, size+1)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("lfs clean: read pointers: %w", err)
		}

		if m := lfsOID.FindSubmatch(body); m != nil && bytes.HasPrefix(body, []byte("version https://git-lfs")) {
			keep[string(m[1])] = struct{}{}
		}
	}

	return keep, nil
}

func git(ctx context.Context, args ...string) ([]byte, error) {
	var stderr bytes.Buffer

	//nolint:gosec // G204: the callers' fixed git subcommands; no shell parses the arguments
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, strings.TrimSpace(stderr.String()))
	}

	return out, nil
}

func (c *client) files(ctx context.Context, repo, dir string) ([]*relicv1.Item, error) {
	r, err := c.artifacts.ListChildren(
		ctx,
		&relicv1.ListChildrenRequest{
			Repository: repo,
			Path:       dir,
		},
	)
	if err != nil {
		return nil, err
	}

	var out []*relicv1.Item

	for _, it := range r.GetItems() {
		if !it.GetFolder() {
			out = append(out, it)

			continue
		}

		below, err := c.files(ctx, repo, it.GetPath())
		if err != nil {
			return nil, err
		}

		out = append(out, below...)
	}

	return out, nil
}
