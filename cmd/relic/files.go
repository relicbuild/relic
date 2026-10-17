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
	"google.golang.org/protobuf/encoding/protojson"

	relicv1 "relic/api/gen/relic/v1"

	"relic/internal/platform/terminal"
)

func splitTarget(s string) (repo, p string, err error) {
	repo, p, _ = strings.Cut(strings.Trim(s, "/"), "/")
	if repo == "" || p == "" {
		return "", "", fmt.Errorf("%q is not <repository>/<path>", s)
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
	var build, moduleID string

	cmd := &cobra.Command{
		Use:   "upload <file> <repository>/<path>",
		Short: "Upload a file",
		Long: "Uploads a file and prints its <repository>/<path> and SHA-256. With --build it is also " +
			"recorded as an artifact of that build run in progress (see relic build).",
		Example: "  relic artifact upload app-1.0.jar libs-local/org/app/1.0/app-1.0.jar\n" +
			"  relic artifact upload target/app.jar libs-local/org/app/1.0/app.jar --build app/42",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, p, err := splitTarget(args[1])
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

			req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPut, c.fileURL(repo, p), f)
			if err != nil {
				return err
			}

			req.ContentLength = st.Size()

			resp, err := c.http.Do(req)
			if err != nil {
				return err
			}

			defer func() {
				_ = resp.Body.Close()
			}()

			if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("upload %s/%s: %w", repo, p, httpError(resp))
			}

			body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if err != nil {
				return err
			}

			item := &relicv1.Item{}
			if err := (protojson.UnmarshalOptions{
				DiscardUnknown: true,
			}).Unmarshal(body, item); err != nil {
				return fmt.Errorf("upload %s/%s: read the answer: %w", repo, p, err)
			}

			item.Name = path.Base(p)

			if build != "" {
				if err := g.addArtifact(build, moduleID, item); err != nil {
					return err
				}
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s/%s %s\n", repo, p, item.GetChecksums().GetSha256())

			return nil
		},
	}
	cmd.Flags().StringVar(&build, "build", "", "record the file as an artifact of this build run, <name>/<number>")
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

			if output == "" {
				output = path.Base(p)
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

			return os.Rename(tmp.Name(), output)
		},
	}
	cmd.Flags().
		StringVarP(&output, "output", "o", "", "file to write (default: the file's name; - for standard output)")

	return cmd
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
