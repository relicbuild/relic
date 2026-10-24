// Package transfer imports a source server into Relic.
// It reads the source over REST and never writes to it.
package transfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"relic/api/adapters/artifactory/access"
	"relic/api/adapters/artifactory/repoconfig"
	relicv1 "relic/api/gen/relic/v1"
	"relic/api/relicapi"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/terminal"
)

// Server is an endpoint: the source, or Relic, written through its own API
// under <URL>/api/.
type Server struct {
	// URL is the platform URL.
	URL string
	// Client carries the credentials.
	Client *http.Client
}

func (s Server) api(p string) string {
	return strings.TrimSuffix(s.URL, "/") + "/artifactory/" + strings.TrimPrefix(p, "/")
}

// Options narrow and shape an import.
type Options struct {
	// Repositories limits the import to these repository keys; empty for
	// all.
	Repositories []string
	// Update replaces repositories, groups and permissions Relic already
	// has.
	Update bool
	// DryRun reports what would be copied and copies nothing.
	DryRun bool
	// Workers copy files in parallel (default 4).
	Workers int
	// Progress receives a line per step; nil for none.
	Progress func(format string, args ...any)
}

// Counts are what an import did with one kind of thing.
type Counts struct {
	Created int `json:"created"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
}

// Report is what an import did.
type Report struct {
	Groups       Counts `json:"groups"`
	Repositories Counts `json:"repositories"`
	Permissions  Counts `json:"permissions"`
	Files        struct {
		Copied  int   `json:"copied"`
		Skipped int   `json:"skipped"`
		Bytes   int64 `json:"bytes"`
	} `json:"files"`
	// Errors are the things that could not be copied.
	Errors []string `json:"errors,omitempty"`
	// Notes are what was copied differently and is left to finish by hand.
	Notes []string `json:"notes,omitempty"`
}

type importer struct {
	src    Server
	relic  relic
	opts   Options
	report Report
	mu     sync.Mutex
}

func (im *importer) progress(format string, args ...any) {
	if im.opts.Progress != nil {
		im.opts.Progress(format, args...)
	}
}

func (im *importer) fail(format string, args ...any) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.report.Errors = append(im.report.Errors, fmt.Sprintf(format, args...))
}

func (im *importer) note(format string, args ...any) {
	im.mu.Lock()
	defer im.mu.Unlock()

	im.report.Notes = append(im.report.Notes, fmt.Sprintf(format, args...))
}

// Import copies the source into Relic.
func Import(ctx context.Context, src, dst Server, opts Options) (Report, error) {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}

	im := &importer{
		src:   src,
		relic: newRelic(dst),
		opts:  opts,
	}

	var repos []repoSummary
	if err := im.getJSON(ctx, src, "api/repositories", &repos); err != nil {
		return im.report, fmt.Errorf("list the source's repositories: %w", err)
	}

	if missing := missingRepositories(repos, opts.Repositories); len(missing) > 0 {
		return im.report, fmt.Errorf("the source has no repository %s", strings.Join(missing, ", "))
	}

	repos = slices.DeleteFunc(repos, func(r repoSummary) bool {
		return len(opts.Repositories) > 0 && !slices.Contains(opts.Repositories, r.Key)
	})

	im.copyGroups(ctx)
	im.copyRepositories(ctx, repos)

	im.copyPermissions(ctx)

	for _, r := range repos {
		if strings.EqualFold(r.Type, "LOCAL") || strings.EqualFold(r.Type, "FEDERATED") {
			im.copyFiles(ctx, r)
		}
	}

	return im.report, nil
}

type repoSummary struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	PackageType string `json:"packageType"`
}

func missingRepositories(repos []repoSummary, names []string) []string {
	var missing []string

	for _, name := range names {
		if !slices.ContainsFunc(repos, func(r repoSummary) bool {
			return r.Key == name
		}) && !slices.Contains(missing, name) {
			missing = append(missing, name)
		}
	}

	return missing
}

func calculated(packageType, name string) bool {
	return name == "maven-metadata.xml" &&
		(strings.EqualFold(packageType, "maven") || strings.EqualFold(packageType, "gradle"))
}

func (im *importer) getJSON(ctx context.Context, s Server, p string, v any) error {
	b, status, err := im.do(ctx, s, http.MethodGet, p, nil, nil)
	if err != nil {
		return err
	}

	if status != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", p, status, snippet(b))
	}

	return json.Unmarshal(b, v)
}

func (im *importer) do(
	ctx context.Context,
	s Server,
	method, p string,
	body []byte,
	header http.Header,
) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.api(p), rd)
	if err != nil {
		return nil, 0, err
	}

	for k, vs := range header {
		req.Header[k] = vs
	}

	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))

	return b, resp.StatusCode, err
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}

	return s
}

func (im *importer) copyGroups(ctx context.Context) {
	var groups []struct {
		Name string `json:"name"`
	}
	if err := im.getJSON(ctx, im.src, "api/security/groups", &groups); err != nil {
		im.fail("groups: %v", err)

		return
	}

	for _, g := range groups {
		var doc groupDoc
		if err := im.getJSON(ctx, im.src, "api/security/groups/"+url.PathEscape(g.Name), &doc); err != nil {
			im.fail("group %s: read: %v", g.Name, err)

			continue
		}

		if err := im.putGroup(ctx, doc); err != nil {
			im.fail("group %s: write: %v", g.Name, err)
		}
	}
}

func (im *importer) copyRepositories(ctx context.Context, repos []repoSummary) {
	copied := map[string]bool{}

	var virtuals []repository.Repository

	for _, summary := range repos {
		r, ok := im.readRepository(ctx, summary.Key)
		if !ok {
			continue
		}

		if r.Class == repository.ClassVirtual {
			virtuals = append(virtuals, r)

			continue
		}

		copied[r.Name] = im.writeRepository(ctx, r)
	}

	for _, r := range membersFirst(virtuals) {
		copied[r.Name] = im.writeRepository(ctx, im.withCopiedMembers(ctx, r, copied))
	}
}

func membersFirst(virtuals []repository.Repository) []repository.Repository {
	byName := make(map[string]repository.Repository, len(virtuals))
	for _, r := range virtuals {
		byName[r.Name] = r
	}

	visited := map[string]bool{}
	ordered := make([]repository.Repository, 0, len(virtuals))

	var visit func(name string)

	visit = func(name string) {
		r, ok := byName[name]
		if !ok || visited[name] {
			return
		}

		visited[name] = true

		for _, member := range r.Members() {
			visit(member)
		}

		ordered = append(ordered, r)
	}

	for _, r := range virtuals {
		visit(r.Name)
	}

	return ordered
}

func (im *importer) withCopiedMembers(
	ctx context.Context,
	r repository.Repository,
	copied map[string]bool,
) repository.Repository {
	if r.Config.Virtual == nil {
		return r
	}

	virtual := *r.Config.Virtual
	virtual.Members = slices.DeleteFunc(slices.Clone(virtual.Members), func(member string) bool {
		if copied[member] || im.destinationHas(ctx, member) {
			return false
		}

		im.note("repository %s: member %s was not imported and is left out", r.Name, member)

		return true
	})

	if virtual.DeployTo != "" && !slices.Contains(virtual.Members, virtual.DeployTo) {
		virtual.DeployTo = ""
	}

	r.Config.Virtual = &virtual

	return r
}

func (im *importer) destinationHas(ctx context.Context, key string) bool {
	_, err := im.relic.repositories.GetRepository(ctx, &relicv1.GetRepositoryRequest{
		Name: key,
	})

	exists, err := found(err)

	return exists && err == nil
}

func (im *importer) readRepository(ctx context.Context, key string) (repository.Repository, bool) {
	doc, status, err := im.do(ctx, im.src, http.MethodGet, "api/repositories/"+url.PathEscape(key), nil, nil)
	if err != nil || status != http.StatusOK {
		im.fail("repository %s: read: %v %d %s", key, err, status, snippet(doc))

		return repository.Repository{}, false
	}

	doc, federated, err := localFromFederated(doc)
	if err != nil {
		im.fail("repository %s: %v", key, err)

		return repository.Repository{}, false
	}

	if federated {
		im.note(
			"repository %s was federated: it is copied as a local repository; federate it again with a "+
				"relic.build/v1 Replication whose direction is federated",
			key,
		)
	}

	r, err := repoconfig.FromArtifactory(key, doc)
	if err != nil {
		im.fail("repository %s: %v", key, err)

		return repository.Repository{}, false
	}

	return r, true
}

func (im *importer) writeRepository(ctx context.Context, r repository.Repository) bool {
	msg := relicapi.RepositoryMessage(r)
	if rm := r.Config.Remote; rm != nil && rm.Password != "" && msg.GetRemote() != nil {
		msg.GetRemote().Password = rm.Password
	}

	if err := im.putRepository(ctx, msg); err != nil {
		im.fail("repository %s: write: %v", r.Name, err)

		return false
	}

	return true
}

func localFromFederated(doc []byte) ([]byte, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc, &fields); err != nil {
		return nil, false, fmt.Errorf("decode: %w", err)
	}

	if string(fields["rclass"]) != `"federated"` {
		return doc, false, nil
	}

	fields["rclass"] = json.RawMessage(`"local"`)
	delete(fields, "members")

	local, err := json.Marshal(fields)
	if err != nil {
		return nil, false, fmt.Errorf("encode: %w", err)
	}

	return local, true, nil
}

func (im *importer) copyPermissions(ctx context.Context) {
	var perms []struct {
		Name string `json:"name"`
	}
	if err := im.getJSON(ctx, im.src, "api/v2/security/permissions", &perms); err != nil {
		im.fail("permissions: %v", err)

		return
	}

	for _, p := range perms {
		doc, status, err := im.do(
			ctx,
			im.src,
			http.MethodGet,
			"api/v2/security/permissions/"+url.PathEscape(p.Name),
			nil,
			nil,
		)
		if err != nil || status != http.StatusOK {
			im.fail("permission %s: read: %v %d %s", p.Name, err, status, snippet(doc))

			continue
		}

		perm, err := access.PermissionFromArtifactory(p.Name, doc)
		if err != nil {
			im.fail("permission %s: %v", p.Name, err)

			continue
		}

		if err := im.putPermission(ctx, relicapi.PermissionMessage(perm)); err != nil {
			im.fail("permission %s: write: %v", p.Name, err)
		}
	}
}

type file struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	SHA1   string `json:"actual_sha1"`
	MD5    string `json:"actual_md5"`
}

func (f file) fullPath() string {
	if f.Path == "." || f.Path == "" {
		return f.Name
	}

	return f.Path + "/" + f.Name
}

const aqlPage = 1000

func (im *importer) copyFiles(ctx context.Context, r repoSummary) {
	repo := r.Key
	last := ""

	for offset := 0; ; offset += aqlPage - 1 {
		query := fmt.Sprintf(
			`items.find({"repo":%q,"type":"file"}).include("repo","path","name","size","sha256","actual_sha1","actual_md5")`+
				`.sort({"$asc":["path","name"]}).offset(%d).limit(%d)`,
			repo,
			offset,
			aqlPage,
		)

		b, status, err := im.do(ctx, im.src, http.MethodPost, "api/search/aql", []byte(query), http.Header{
			"Content-Type": {
				"text/plain",
			},
		})
		if err != nil || status != http.StatusOK {
			im.fail("files of %s: list: %v %d %s", repo, err, status, snippet(b))

			return
		}

		var page struct {
			Results []file `json:"results"`
		}
		if err := json.Unmarshal(b, &page); err != nil {
			im.fail("files of %s: list: %v", repo, err)

			return
		}

		listed := page.Results
		if offset > 0 {
			if len(listed) > 0 && listed[0].fullPath() == last {
				listed = listed[1:]
			} else {
				im.fail(
					"files of %s: the repository changed while it was listed; run again to copy what was missed",
					repo,
				)
			}
		}

		if len(page.Results) > 0 {
			last = page.Results[len(page.Results)-1].fullPath()
		}

		files := slices.DeleteFunc(slices.Clone(listed), func(f file) bool {
			return calculated(r.PackageType, f.Name)
		})
		im.copyBatch(ctx, files)

		if len(page.Results) < aqlPage {
			return
		}
	}
}

func (im *importer) copyBatch(ctx context.Context, files []file) {
	var (
		next int64 = -1
		wg   sync.WaitGroup
	)

	for range im.opts.Workers {
		wg.Go(func() {
			for {
				i := atomic.AddInt64(&next, 1)
				if int(i) >= len(files) || ctx.Err() != nil {
					return
				}

				im.copyFile(ctx, files[i])
			}
		})
	}

	wg.Wait()
}

func storagePath(repo, p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}

	return url.PathEscape(repo) + "/" + strings.Join(segs, "/")
}

func (im *importer) copyFile(ctx context.Context, f file) {
	name := f.Repo + "/" + f.fullPath()
	target := storagePath(f.Repo, f.fullPath())

	if im.stored(ctx, f) {
		im.count(func(r *Report) {
			r.Files.Skipped++
		})

		return
	}

	if im.opts.DryRun {
		im.progress("would copy %s (%s)", name, terminal.ByteSize(f.Size))
		im.count(func(r *Report) {
			r.Files.Copied++
			r.Files.Bytes += f.Size
		})

		return
	}

	props, err := im.sourceProperties(ctx, target)
	if err != nil {
		im.fail("file %s: properties: %v", name, err)

		return
	}

	if err := im.stream(ctx, f, target); err != nil {
		im.fail("file %s: %v", name, err)

		return
	}

	if err := im.setProperties(ctx, f, props); err != nil {
		im.fail("file %s: properties: %v", name, err)

		if err := im.removeFile(ctx, f); err != nil {
			im.fail("file %s: kept without its properties, delete it to copy it again: %v", name, err)
		}

		return
	}

	im.count(func(r *Report) {
		r.Files.Copied++
		r.Files.Bytes += f.Size
	})
	im.progress("copied %s", name)
}

func (im *importer) count(f func(*Report)) {
	im.mu.Lock()
	defer im.mu.Unlock()

	f(&im.report)
}

func (im *importer) stream(ctx context.Context, f file, target string) error {
	get, err := http.NewRequestWithContext(ctx, http.MethodGet, im.src.api(target), nil)
	if err != nil {
		return err
	}

	resp, err := im.src.Client.Do(get)
	if err != nil {
		return err
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))

		return fmt.Errorf("download: %d %s", resp.StatusCode, snippet(b))
	}

	put, err := http.NewRequestWithContext(ctx, http.MethodPut, im.relic.files(target), resp.Body)
	if err != nil {
		return err
	}

	put.ContentLength = resp.ContentLength

	for h, v := range map[string]string{
		"X-Checksum-Sha256": f.SHA256,
		"X-Checksum-Sha1":   f.SHA1,
		"X-Checksum-Md5":    f.MD5,
	} {
		if v != "" {
			put.Header.Set(h, v)
		}
	}

	up, err := im.relic.client.Do(put)
	if err != nil {
		return err
	}

	defer func() {
		_ = up.Body.Close()
	}()

	if up.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(up.Body, 1<<10))

		return fmt.Errorf("upload: %d %s", up.StatusCode, snippet(b))
	}

	return nil
}

func (im *importer) sourceProperties(ctx context.Context, target string) (map[string][]string, error) {
	b, status, err := im.do(ctx, im.src, http.MethodGet, "api/storage/"+target+"?properties", nil, nil)
	if err != nil {
		return nil, err
	}

	switch status {
	case http.StatusNotFound:
		return nil, nil
	case http.StatusOK:
	default:
		return nil, fmt.Errorf("read: %d %s", status, snippet(b))
	}

	var doc struct {
		Properties map[string][]string `json:"properties"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	return doc.Properties, nil
}

// ErrNoSource is an import without a source URL.
var ErrNoSource = errors.New("name the source Artifactory's URL")

func normalize(u string) string {
	u = strings.TrimSuffix(u, "/")

	return strings.TrimSuffix(u, "/artifactory")
}
