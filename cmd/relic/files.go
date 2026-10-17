package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

func splitTarget(s string) (repo, p string, err error) {
	repo, p, _ = strings.Cut(strings.Trim(s, "/"), "/")
	if repo == "" || p == "" {
		return "", "", fmt.Errorf("%q is not <repository>/<path>", s)
	}

	for segment := range strings.SplitSeq(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", fmt.Errorf("invalid path %q: it has an empty, \".\" or \"..\" segment", s)
		}
	}

	return repo, p, nil
}

func (c *client) fileURL(repo, p string) string { return c.itemURL("/api/v1/files/", repo, p) }

// itemURL is the URL of an item under one of the Relic API's plain HTTP
// prefixes.
func (c *client) itemURL(prefix, repo, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}

	return c.url + prefix + url.PathEscape(repo) + "/" + strings.Join(segs, "/")
}

func httpError(resp *http.Response) error {
	var body struct {
		Message string
	}

	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if json.Unmarshal(b, &body) == nil && body.Message != "" {
		return fmt.Errorf("%s: %s", resp.Status, body.Message)
	}

	return fmt.Errorf("%s", resp.Status)
}

func uploadCmd(g *globals) *cobra.Command {
	var (
		build    buildRunFlag
		moduleID string
	)

	cmd := &cobra.Command{
		Use:   "upload <file> <repository>/<path>",
		Short: "Upload a file",
		Long: "Uploads a file and prints its <repository>/<path> and SHA-256. A destination ending in / is " +
			"a folder the file keeps its name in. With --build it is also " +
			"recorded as an artifact of that build run in progress (see relic build).",
		Example: "  relic artifact upload app-1.0.jar libs-local/org/app/1.0/app-1.0.jar\n" +
			"  relic artifact upload dist/app.tar.gz libs-local/app/\n" +
			"  relic artifact upload target/app.jar libs-local/org/app/1.0/app.jar --build app/42",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[1]
			if strings.HasSuffix(target, "/") {
				target += filepath.Base(args[0])
			}

			repo, p, err := splitTarget(target)
			if err != nil {
				return err
			}

			f, err := os.Open(args[0])
			if err != nil {
				return err
			}

			defer func() {
				_ = f.Close()
			}()

			st, err := f.Stat()
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			var props url.Values
			if build.value != "" {
				if props, err = g.buildProperties(build.value); err != nil {
					return err
				}
			}

			item, err := c.put(cmd.Context(), repo, p, f, st.Size(), props)
			if err != nil {
				return err
			}

			if build.value != "" {
				if err := g.addArtifact(build.value, moduleID, item); err != nil {
					return err
				}
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s %s\n", repo, p, item.GetChecksums().GetSha256())

			return nil
		},
	}
	cmd.Flags().Var(&build, "build", "record the file as an artifact of this build run, <name>/<number>")
	cmd.Flags().StringVar(&moduleID, "module", "", "the build module it belongs to (default: the build's name)")

	return cmd
}

func downloadCmd(g *globals) *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:     "download <repository>/<path>",
		Short:   "Download a file",
		Example: "  relic artifact download libs-local/org/app/1.0/app-1.0.jar\n  relic artifact download libs-local/notes.txt -o - | less",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[0])
			if err != nil {
				return err
			}

			if output != "-" {
				if output, err = downloadPath(output, path.Base(p)); err != nil {
					return fmt.Errorf("download %s/%s: %w", repo, p, err)
				}
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, c.fileURL(repo, p), nil)
			if err != nil {
				return err
			}

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("download %s/%s: %w", repo, p, httpError(resp))
			}

			if output == "-" {
				_, err := io.Copy(cmd.OutOrStdout(), resp.Body)
				return err
			}

			tmp, err := os.CreateTemp(filepath.Dir(output), ".relic-download-*")
			if err != nil {
				return err
			}

			defer func() {
				_ = os.Remove(tmp.Name())
			}()

			if _, err := io.Copy(tmp, resp.Body); err != nil {
				_ = tmp.Close()
				return fmt.Errorf("download %s/%s: %w", repo, p, err)
			}

			if err := tmp.Close(); err != nil {
				return err
			}

			if err := os.Chmod(tmp.Name(), 0o644); err != nil { //nolint:gosec // a downloaded file, like curl -O writes
				return err
			}

			if err := os.Rename(tmp.Name(), output); err != nil {
				return fmt.Errorf("download %s/%s: cannot write %s", repo, p, output)
			}

			return nil
		},
	}
	cmd.Flags().
		StringVarP(
			&output,
			"output",
			"o",
			"",
			"file to write, or a directory to write it into (default: the file's name; - for standard output)",
		)

	return cmd
}

// downloadPath is the file a download of name writes for -o output: output
// itself, or name inside it when it is a directory or ends in a separator.
func downloadPath(output, name string) (string, error) {
	switch {
	case output == "":
		return name, nil
	case strings.HasSuffix(output, "/") || strings.HasSuffix(output, string(filepath.Separator)):
		output = filepath.Join(output, name)
	default:
		if st, err := os.Stat(output); err == nil && st.IsDir() {
			output = filepath.Join(output, name)
		}
	}

	if st, err := os.Stat(filepath.Dir(output)); err != nil || !st.IsDir() {
		return "", fmt.Errorf("no directory %s to write %s into", filepath.Dir(output), filepath.Base(output))
	}

	return output, nil
}

func searchCmd(g *globals) *cobra.Command {
	var (
		repo   string
		limit  int32
		output string
	)

	cmd := &cobra.Command{
		Use:     "search <name>",
		Short:   "Find files and folders by name",
		Long:    "Lists the items whose name contains <name> (at least two characters, case-insensitive) that you may read.",
		Example: "  relic artifact search app-1.0 --repository libs-local",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := g.dial()
			if err != nil {
				return err
			}

			r, err := c.artifacts.SearchItems(
				cmd.Context(),
				connect.NewRequest(&relicv1.SearchItemsRequest{
					Name:       args[0],
					Repository: repo,
					Limit:      limit,
				}),
			)
			if err != nil {
				return fmt.Errorf("search: %w", err)
			}

			return printMessage(cmd.OutOrStdout(), output, r.Msg, func(w io.Writer) error {
				table := terminal.Table{
					Headers: []string{
						"ITEM",
						"TYPE",
						"SIZE",
						"MODIFIED",
						"BY",
					},
					RightAligned: []int{
						2,
					},
				}

				for _, item := range r.Msg.GetItems() {
					kind, size := "file", terminal.ByteSize(item.GetSize())
					if item.GetFolder() {
						kind, size = "folder", ""
					}

					table.Rows = append(table.Rows, []string{
						item.GetRepository() + "/" + item.GetPath(),
						kind,
						size,
						localTime(item.GetModified(), time.DateTime),
						item.GetModifiedBy(),
					})
				}

				return table.Print(w)
			})
		},
	}
	cmd.Flags().StringVar(&repo, "repository", "", "search one repository only")
	cmd.Flags().Int32Var(&limit, "limit", 0, "results per repository (default 100)")
	outputFlag(cmd, &output)

	return cmd
}
