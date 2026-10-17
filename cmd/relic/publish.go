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
	"net/url"
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

	hf "relic/internal/artifacts/formats/huggingface"
)

func (c *client) put(
	ctx context.Context,
	repo, p string,
	body io.Reader,
	size int64,
	props url.Values,
) (*relicv1.Item, error) {
	target := c.fileURL(repo, p)
	if len(props) > 0 {
		target += "?" + props.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, body)
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

func (c *client) putBytes(ctx context.Context, repo, p string, b []byte, props url.Values) (*relicv1.Item, error) {
	return c.put(ctx, repo, p, bytes.NewReader(b), int64(len(b)), props)
}

type publishFlags struct {
	build  buildRunFlag
	module string
}

// properties are the properties of the files published, none without --build.
func (f *publishFlags) properties(g *globals) (url.Values, error) {
	if f.build.value == "" {
		return nil, nil
	}

	return g.buildProperties(f.build.value)
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

			if err := c.requireFormat(cmd.Context(), repo, relicv1.Format_FORMAT_GOMOD); err != nil {
				return fmt.Errorf("go publish: %w", err)
			}

			base := modPath + "/@v/" + version

			props, err := pf.properties(g)
			if err != nil {
				return err
			}

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
				it, err := c.putBytes(cmd.Context(), repo, base+f.ext, f.body, props)
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

			if err := c.requireFormat(cmd.Context(), repo, relicv1.Format_FORMAT_TERRAFORM); err != nil {
				return fmt.Errorf("terraform publish: %w", err)
			}

			p := namespace + "/" + name + "/" + provider + "/v" + version + ".zip"

			props, err := pf.properties(g)
			if err != nil {
				return err
			}

			it, err := c.putBytes(cmd.Context(), repo, p, archive, props)
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

		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}

		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate

		w, err := zw.CreateHeader(header)
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
		Long: "Models and datasets in a Hugging Face repository are stored as commits of revisions. Upload " +
			"writes commits through the Relic API and download reads through the repository's Hub API; " +
			"neither needs huggingface_hub.",
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
			"  relic package hf upload ./data hf-local acme/reviews --dataset --revision v2",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, repo, id := args[0], args[1], strings.Trim(args[2], "/")
			ctx := cmd.Context()

			if !hf.ValidID(id) {
				return fmt.Errorf("huggingface upload: %q is not a model or dataset id (<name> or <owner>/<name>)", id)
			}

			if !hf.ValidRevision(revision) {
				return fmt.Errorf(
					"huggingface upload: %q is not a revision: letters, digits, '.', '_' and '-', with no '/'",
					revision,
				)
			}

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

			if err := c.requireFormat(ctx, repo, relicv1.Format_FORMAT_HUGGINGFACE); err != nil {
				return fmt.Errorf("huggingface upload: %w", err)
			}

			base := hfKind(dataset) + "/" + id

			props, err := pf.properties(g)
			if err != nil {
				return err
			}

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

				it, err := c.put(ctx, repo, commit+"/"+f, fh, st.Size(), props)
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
		Use:   "download <repository> <id> <directory>",
		Short: "Download a model or dataset revision into a directory",
		Long: "Reads the revision through the repository's Hub API, as huggingface_hub's snapshot_download " +
			"does, so it works with local, remote and virtual repositories.",
		Example: "  relic package huggingface download hf-remote google-bert/bert-base-uncased ./bert",
		Args:    cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			repo, id, dir := args[0], strings.Trim(args[1], "/"), args[2]
			ctx := cmd.Context()

			c, err := g.dial()
			if err != nil {
				return err
			}

			if err := c.requireFormat(ctx, repo, relicv1.Format_FORMAT_HUGGINGFACE); err != nil {
				return fmt.Errorf("huggingface download: %w", err)
			}

			hub := c.url + "/huggingface/" + url.PathEscape(repo)

			snapshot, err := c.hubRevision(ctx, hub, hfKind(dataset), id, revision)

			switch {
			case errors.Is(err, errHubRevisionNotFound):
				return fmt.Errorf("huggingface download: %s has no revision %s in %s", id, revision, repo)
			case err != nil:
				return fmt.Errorf("huggingface download: %w", err)
			}

			resolve := hub + "/" + escapedSegments(id) + "/resolve/" + url.PathEscape(snapshot.SHA) + "/"
			if dataset {
				resolve = hub + "/datasets/" + escapedSegments(id) + "/resolve/" + url.PathEscape(snapshot.SHA) + "/"
			}

			for _, sibling := range snapshot.Siblings {
				rel := sibling.Rfilename
				if !filepath.IsLocal(filepath.FromSlash(rel)) {
					return fmt.Errorf("huggingface download: unsafe path %q", rel)
				}

				target := filepath.Join(dir, filepath.FromSlash(rel))
				if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
					return err
				}

				if err := c.downloadURL(ctx, resolve+escapedSegments(rel), target); err != nil {
					return fmt.Errorf("huggingface download: %s: %w", rel, err)
				}
			}

			_, _ = fmt.Fprintf(
				cmd.OutOrStdout(),
				"%s@%s: %d files in %s\n",
				id,
				revision,
				len(snapshot.Siblings),
				dir,
			)

			return nil
		},
	}
	cmd.Flags().StringVar(&revision, "revision", "main", "the revision to download")
	cmd.Flags().BoolVar(&dataset, "dataset", false, "the id names a dataset, not a model")

	return cmd
}

func (c *client) requireFormat(ctx context.Context, repo string, formats ...relicv1.Format) error {
	r, err := c.repositories.GetRepository(ctx, connect.NewRequest(&relicv1.GetRepositoryRequest{
		Name: repo,
	}))
	if err != nil {
		return err
	}

	if format := r.Msg.GetRepository().GetFormat(); !slices.Contains(formats, format) {
		return fmt.Errorf("%s is a %s repository", repo, formatName(format))
	}

	return nil
}

var errHubRevisionNotFound = errors.New("revision not found")

type hubSnapshot struct {
	SHA      string `json:"sha"`
	Siblings []struct {
		Rfilename string `json:"rfilename"`
	} `json:"siblings"`
}

func (c *client) hubRevision(ctx context.Context, hub, kind, id, revision string) (hubSnapshot, error) {
	u := hub + "/api/" + kind + "/" + escapedSegments(id) + "/revision/" + url.PathEscape(revision)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return hubSnapshot{}, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return hubSnapshot{}, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch {
	case resp.StatusCode == http.StatusOK:
	case slices.Contains([]string{
		"RevisionNotFound",
		"RepoNotFound",
	}, resp.Header.Get("X-Error-Code")):
		return hubSnapshot{}, errHubRevisionNotFound
	default:
		return hubSnapshot{}, hubError(resp)
	}

	var snapshot hubSnapshot
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&snapshot); err != nil {
		return hubSnapshot{}, fmt.Errorf("read the revision: %w", err)
	}

	if snapshot.SHA == "" {
		return hubSnapshot{}, errHubRevisionNotFound
	}

	return snapshot, nil
}

func hubError(resp *http.Response) error {
	if message := resp.Header.Get("X-Error-Message"); message != "" {
		return fmt.Errorf("%s: %s", resp.Status, message)
	}

	return httpError(resp)
}

func escapedSegments(p string) string {
	segments := strings.Split(p, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}

	return strings.Join(segments, "/")
}

func (c *client) downloadURL(ctx context.Context, u, target string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return hubError(resp)
	}

	return writeFileAtomic(target, resp.Body, 0o644)
}
