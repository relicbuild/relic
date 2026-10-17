package main

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"crypto/sha1" //nolint:gosec // a commit id like git's, not a security use
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/mod/modfile"
	gomodule "golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
	"google.golang.org/protobuf/encoding/protojson"

	relicv1 "relic/api/gen/relic/v1"
)

func (c *client) put(ctx context.Context, repo, p string, body io.Reader, size int64) (*relicv1.Item, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.fileURL(repo, p), body)
	if err != nil {
		return nil, err
	}

	req.ContentLength = size

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upload %s/%s: %w", repo, p, httpError(resp))
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	it := &relicv1.Item{}
	if err := unmarshalItem(b, it); err != nil {
		return nil, fmt.Errorf("upload %s/%s: read the answer: %w", repo, p, err)
	}

	it.Name = path.Base(p)

	return it, nil
}

func unmarshalItem(b []byte, it *relicv1.Item) error {
	return protojson.UnmarshalOptions{
		DiscardUnknown: true,
	}.Unmarshal(b, it)
}

func (c *client) putBytes(ctx context.Context, repo, p string, b []byte) (*relicv1.Item, error) {
	return c.put(ctx, repo, p, bytes.NewReader(b), int64(len(b)))
}

type publishFlags struct {
	build  buildRunFlag
	module string
}

func (f *publishFlags) register(cmd *cobra.Command) {
	cmd.Flags().Var(&f.build, "build", "record the files as artifacts of this build run, <name>/<number>")
	cmd.Flags().StringVar(&f.module, "module", "", "the build module they belong to (default: the build's name)")
}

func (f *publishFlags) record(g *globals, items ...*relicv1.Item) error {
	if f.build.value == "" {
		return nil
	}

	for _, it := range items {
		if err := g.addArtifact(f.build.value, f.module, it); err != nil {
			return err
		}
	}

	return nil
}

func publishCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publish Go and Terraform modules from a directory",
	}
	cmd.AddCommand(goPublishCmd(g), terraformPublishCmd(g))

	return cmd
}

func goPublishCmd(g *globals) *cobra.Command {
	var (
		dir string
		pf  publishFlags
	)

	cmd := &cobra.Command{
		Use:   "go <repository> <version>",
		Short: "Publish the Go module in a directory as a version",
		Long: "Builds the module zip the go command expects (golang.org/x/mod/zip: the same files, limits and " +
			"checks) and uploads it with go.mod and the version's .info, so go get fetches it through the repository.",
		Example:           "  relic package publish go go-local v1.4.0\n  relic package publish go go-local v1.4.0 --dir ./lib --build lib/12",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeRepositoriesAt(g, 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, version := args[0], args[1]

			gomod, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // the module's go.mod
			if err != nil {
				return err
			}

			modPath := modfile.ModulePath(gomod)
			if modPath == "" {
				return errors.New("go publish: go.mod names no module")
			}

			mv := gomodule.Version{
				Path:    modPath,
				Version: version,
			}
			if err := gomodule.Check(mv.Path, mv.Version); err != nil {
				return fmt.Errorf("go publish: %w", err)
			}

			var zipped bytes.Buffer
			if err := modzip.CreateFromDir(&zipped, mv, dir); err != nil {
				return fmt.Errorf("go publish: %w", err)
			}

			info, err := json.Marshal(map[string]string{
				"Version": version,
				"Time":    time.Now().UTC().Format(time.RFC3339),
			})
			if err != nil {
				return err
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			base := modPath + "/@v/" + version

			var items []*relicv1.Item

			for _, f := range []struct {
				ext  string
				body []byte
			}{
				{
					ext:  ".mod",
					body: gomod,
				},
				{
					ext:  ".info",
					body: info,
				},
				{
					ext:  ".zip",
					body: zipped.Bytes(),
				},
			} {
				it, err := c.putBytes(cmd.Context(), repo, base+f.ext, f.body)
				if err != nil {
					return fmt.Errorf("go publish: %w", err)
				}

				items = append(items, it)
			}

			if err := pf.record(g, items...); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s published to %s\n", modPath, version, repo)

			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "the module's directory")
	pf.register(cmd)

	return cmd
}

var skipInModules = []string{
	".git",
	".terraform",
	".terraform.lock.hcl",
	"terraform.tfstate",
	"terraform.tfstate.backup",
}

func terraformPublishCmd(g *globals) *cobra.Command {
	var (
		dir, namespace, provider, name string
		pf                             publishFlags
	)

	cmd := &cobra.Command{
		Use:   "terraform <repository> <version>",
		Short: "Publish the Terraform module in a directory as a version",
		Long: "Zips the module (leaving out .git, .terraform and state files) and uploads it where the module " +
			"registry serves it: source = \"<host>/<repository>__<namespace>/<name>/<provider>\".",
		Example: "  relic package publish terraform tf-local 1.0.0 --namespace acme --provider aws --dir modules/network",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, version := args[0], strings.TrimPrefix(args[1], "v")

			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}

			name = cmp.Or(name, filepath.Base(abs))

			archive, err := zipModule(abs)
			if err != nil {
				return fmt.Errorf("terraform publish: %w", err)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			p := namespace + "/" + name + "/" + provider + "/v" + version + ".zip"

			it, err := c.putBytes(cmd.Context(), repo, p, archive)
			if err != nil {
				return fmt.Errorf("terraform publish: %w", err)
			}

			if err := pf.record(g, it); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s/%s/%s %s published to %s\n",
				namespace,
				name,
				provider,
				version,
				repo,
			)

			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "the module's directory")
	cmd.Flags().StringVar(&namespace, "namespace", "", "the module's namespace")
	cmd.Flags().StringVar(&provider, "provider", "", "the module's provider (aws, azurerm, …)")
	cmd.Flags().StringVar(&name, "name", "", "the module's name (default: the directory's)")
	_ = cmd.MarkFlagRequired("namespace")
	_ = cmd.MarkFlagRequired("provider")
	pf.register(cmd)

	return cmd
}

func zipModule(dir string) ([]byte, error) {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}

		if slices.Contains(skipInModules, d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}

		w, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}

		f, err := os.Open(p) //nolint:gosec // a file of the module being published
		if err != nil {
			return err
		}

		defer func() {
			_ = f.Close()
		}()

		_, err = io.Copy(w, f)

		return err
	})
	if err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func huggingFaceCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use: "huggingface",
		Aliases: []string{
			"hf",
		},
		Short: "Upload and download Hugging Face models and datasets",
		Long: "Models and datasets in a Hugging Face repository are stored as commits of revisions. These " +
			"commands work through the Relic API, without huggingface_hub.",
	}
	cmd.AddCommand(
		huggingFaceUploadCmd(g),
		huggingFaceDownloadCmd(g),
	)

	return cmd
}

func hfKind(dataset bool) string {
	if dataset {
		return "datasets"
	}

	return "models"
}

const hfRevTime = "2006-01-02T15:04:05.000Z"

type hfCommit struct {
	dir  string
	time time.Time
}

func (c *client) latestCommit(ctx context.Context, repo, base, rev string) (hfCommit, bool, error) {
	r, err := c.artifacts.ListChildren(ctx, connect.NewRequest(&relicv1.ListChildrenRequest{
		Repository: repo,
		Path:       base,
	}))
	if isNotFound(err) {
		return hfCommit{}, false, nil
	}

	if err != nil {
		return hfCommit{}, false, err
	}

	var (
		latest hfCommit
		found  bool
	)

	for _, it := range r.Msg.GetItems() {
		name, stamp, ok := cutLast(it.GetName(), "_")
		if !it.GetFolder() || !ok || name != rev {
			continue
		}

		t, err := time.Parse(hfRevTime, stamp)
		if err != nil || (found && !t.After(latest.time)) {
			continue
		}

		kids, err := c.artifacts.ListChildren(ctx, connect.NewRequest(&relicv1.ListChildrenRequest{
			Repository: repo,
			Path:       it.GetPath(),
		}))
		if err != nil {
			return hfCommit{}, false, err
		}

		for _, k := range kids.Msg.GetItems() {
			if k.GetFolder() && len(k.GetName()) == 40 {
				latest, found = hfCommit{
					dir:  k.GetPath(),
					time: t,
				}, true
			}
		}
	}

	return latest, found, nil
}

func cutLast(s, sep string) (before, after string, found bool) {
	i := strings.LastIndex(s, sep)
	if i <= 0 {
		return s, "", false
	}

	return s[:i], s[i+len(sep):], true
}

func huggingFaceUploadCmd(g *globals) *cobra.Command {
	var (
		revision string
		dataset  bool
		pf       publishFlags
	)

	cmd := &cobra.Command{
		Use:   "upload <directory> <repository> <id>",
		Short: "Upload a directory as a new commit of a model or dataset",
		Long: "Makes a commit of the revision holding the previous commit's files and the directory's " +
			"(which replace files of the same path), as huggingface_hub's upload_folder does.",
		Example: "  relic package huggingface upload ./bert-tiny hf-local acme/bert-tiny\n" +
			"  relic hf upload ./data hf-local acme/reviews --dataset --revision v2",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, repo, id := args[0], args[1], strings.Trim(args[2], "/")
			ctx := cmd.Context()

			var files []string

			err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}

				if d.IsDir() && d.Name() == ".git" {
					return filepath.SkipDir
				}

				if d.Type().IsRegular() {
					rel, err := filepath.Rel(dir, p)
					if err != nil {
						return err
					}

					files = append(files, filepath.ToSlash(rel))
				}

				return nil
			})
			if err != nil {
				return err
			}

			if len(files) == 0 {
				return fmt.Errorf("huggingface upload: %s has no files", dir)
			}

			c, err := g.dial()
			if err != nil {
				return err
			}

			base := hfKind(dataset) + "/" + id

			prev, hasPrev, err := c.latestCommit(ctx, repo, base, revision)
			if err != nil {
				return fmt.Errorf("huggingface upload: %w", err)
			}

			now := time.Now().UTC()
			h := sha1.New() //nolint:gosec // see the import

			_, _ = fmt.Fprintf(h, "%s\n%s\n%s\n%d\n", hfKind(dataset), id, revision, now.UnixNano())
			for _, f := range files {
				_, _ = fmt.Fprintf(h, "%s\n", f)
			}

			commit := base + "/" + revision + "_" + now.Format(hfRevTime) + "/" + hex.EncodeToString(h.Sum(nil))

			discard := func(err error) error {
				_, derr := c.artifacts.DeleteItem(
					context.WithoutCancel(ctx),
					connect.NewRequest(&relicv1.DeleteItemRequest{
						Repository: repo,
						Path:       commit,
					}),
				)
				if derr != nil && !isNotFound(derr) {
					return errors.Join(
						err,
						fmt.Errorf("huggingface upload: remove the partial commit %s: %w", commit, derr),
					)
				}

				return err
			}

			if hasPrev {
				if _, err := c.artifacts.CopyItem(ctx, connect.NewRequest(&relicv1.CopyItemRequest{
					Repository:   repo,
					Path:         prev.dir,
					ToRepository: repo,
					ToPath:       commit,
				})); err != nil {
					return discard(fmt.Errorf("huggingface upload: copy the previous commit: %w", err))
				}
			}

			var items []*relicv1.Item

			for _, f := range files {
				fh, err := os.Open(filepath.Join(dir, filepath.FromSlash(f))) //nolint:gosec // a file being uploaded
				if err != nil {
					return discard(err)
				}

				st, err := fh.Stat()
				if err != nil {
					_ = fh.Close()

					return discard(err)
				}

				it, err := c.put(ctx, repo, commit+"/"+f, fh, st.Size())
				_ = fh.Close()

				if err != nil {
					return discard(fmt.Errorf("huggingface upload: %w", err))
				}

				items = append(items, it)
			}

			if err := pf.record(g, items...); err != nil {
				return err
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s: %d files committed to %s\n", id, revision, len(files), repo)

			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "main", "the revision (branch) to commit to")
	cmd.Flags().BoolVar(&dataset, "dataset", false, "the id names a dataset, not a model")
	pf.register(cmd)

	return cmd
}

func huggingFaceDownloadCmd(g *globals) *cobra.Command {
	var (
		revision string
		dataset  bool
	)

	cmd := &cobra.Command{
		Use:     "download <repository> <id> <directory>",
		Short:   "Download a model or dataset revision into a directory",
		Example: "  relic package huggingface download hf-remote google-bert/bert-base-uncased ./bert",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, id, dir := args[0], strings.Trim(args[1], "/"), args[2]
			ctx := cmd.Context()

			c, err := g.dial()
			if err != nil {
				return err
			}

			latest, ok, err := c.latestCommit(ctx, repo, hfKind(dataset)+"/"+id, revision)
			if err != nil {
				return fmt.Errorf("huggingface download: %w", err)
			}

			if !ok {
				return fmt.Errorf("huggingface download: %s has no revision %s in %s", id, revision, repo)
			}

			files, err := c.files(ctx, repo, latest.dir)
			if err != nil {
				return fmt.Errorf("huggingface download: %w", err)
			}

			for _, it := range files {
				rel := strings.TrimPrefix(it.GetPath(), latest.dir+"/")

				target := filepath.Join(dir, filepath.FromSlash(rel))
				if !filepath.IsLocal(filepath.FromSlash(rel)) {
					return fmt.Errorf("huggingface download: unsafe path %q", rel)
				}

				if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
					return err
				}

				if err := c.download(ctx, repo, it.GetPath(), target); err != nil {
					return fmt.Errorf("huggingface download: %w", err)
				}
			}

			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "%s@%s: %d files in %s\n", id, revision, len(files), dir)

			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "main", "the revision to download")
	cmd.Flags().BoolVar(&dataset, "dataset", false, "the id names a dataset, not a model")

	return cmd
}

func (c *client) download(ctx context.Context, repo, p, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.fileURL(repo, p), nil)
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

	return writeFileAtomic(target, resp.Body, 0o644)
}
