package packages

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/conan"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

const conanCapabilities = "complex_search,checksum_deploy,revisions,matrix_params"

const conanTokenTTL = 24 * time.Hour

func (h *Handler) conanPing(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Conan-Server-Capabilities", conanCapabilities)
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) conanAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != conan.Format {
		httperror.Write(w, r, http.StatusNotFound, "repository not found")
		return
	}

	w.Header().Set("X-Conan-Server-Capabilities", conanCapabilities)

	switch {
	case rest == "v2/users/authenticate" && r.Method == http.MethodGet:
		h.conanAuthenticate(w, r)
	case rest == "v2/users/check_credentials" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(auth.FromContext(r.Context()).Name())) //nolint:gosec // plain text user name
	case rest == "v2/conans/search" && r.Method == http.MethodGet:
		h.conanSearch(w, r, rp)
	case strings.HasPrefix(rest, "v2/conans/"):
		parts := strings.Split(strings.TrimPrefix(rest, "v2/conans/"), "/")
		if len(parts) < 5 {
			httperror.Write(w, r, http.StatusNotFound, "not found")
			return
		}

		ref, err := conan.ParseRef(parts[0], parts[1], parts[2], parts[3])
		if err != nil {
			httperror.Write(w, r, http.StatusBadRequest, err.Error())
			return
		}

		h.conanRef(w, r, rp, ref, parts[4:])
	default:
		httperror.Write(w, r, http.StatusNotFound, "not found")
	}
}

func (h *Handler) conanAuthenticate(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	token := ""

	if _, secret, ok := r.BasicAuth(); ok {
		if _, err := h.tokens.Verify(r.Context(), secret); err == nil {
			token = secret
		}
	}

	if token == "" {
		resp, err := h.tokens.Issue(r.Context(), p.LimitIssue(auth.IssueParams{
			Username:    p.Name(),
			Scope:       auth.UserScope,
			ExpiresIn:   conanTokenTTL,
			Description: "conan remote login",
		}, time.Now()))
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		token = resp.AccessToken
	}

	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(token)) //nolint:gosec // the token is the response
}

func (h *Handler) conanSearch(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}) {
		return
	}

	q := r.URL.Query()
	seen := map[string]bool{}
	results := []string{}

	for _, m := range h.conan.Members(r.Context(), rp) {
		var refs []string

		if m.Class == "remote" {
			raw, err := h.conan.Upstream(r.Context(), m, "v2/conans/search?q="+url.QueryEscape(q.Get("q")))

			var v struct {
				Results []string `json:"results"`
			}
			if err == nil && json.Unmarshal(raw, &v) == nil {
				refs = visibleRefs(r.Context(), m.Name, v.Results)
			}
		} else {
			var err error
			if refs, err = h.conan.Search(r.Context(), m.Name, q.Get("q"), q.Get("ignorecase") != "False"); err != nil {
				h.writeError(w, r, err)
				return
			}
		}

		for _, ref := range refs {
			if !seen[ref] {
				seen[ref] = true
				results = append(results, ref)
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
	})
}

func (h *Handler) conanRef(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	ref conan.Ref,
	p []string,
) {
	switch r.Method {
	case http.MethodPut:
		h.conanUpload(w, r, rp, ref, p)
		return
	case http.MethodDelete:
		h.conanDelete(w, r, rp, ref, p)
		return
	case http.MethodGet, http.MethodHead:
	default:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		ref.Dir(),
		auth.ActionRead,
	}) {
		return
	}

	api := "v2/conans/" + ref.APIPath() + "/" + strings.Join(p, "/")
	switch {
	case len(p) == 1 && p[0] == "latest":
		h.conanLatest(w, r, rp, api, ref.Dir(), func(ctx context.Context, key string) ([]conan.Revision, error) {
			return h.conan.RecipeRevisions(ctx, key, ref)
		})
	case len(p) == 1 && p[0] == "revisions":
		h.conanRevisions(w, r, rp, api, ref.Dir(), func(ctx context.Context, key string) ([]conan.Revision, error) {
			return h.conan.RecipeRevisions(ctx, key, ref)
		})
	case len(p) == 3 && p[0] == "revisions" && p[2] == "search":
		h.conanPackages(w, r, rp, api, ref, p[1])
	case len(p) == 3 && p[0] == "revisions" && p[2] == "files":
		h.conanFiles(w, r, rp, api, path.Join(ref.Dir(), p[1], "export"))
	case len(p) >= 4 && p[0] == "revisions" && p[2] == "files":
		h.conanDownload(w, r, rp, api, path.Join(ref.Dir(), p[1], "export", strings.Join(p[3:], "/")))
	case len(p) == 5 && p[0] == "revisions" && p[2] == "packages" && p[4] == "latest":
		h.conanLatest(
			w,
			r,
			rp,
			api,
			path.Join(ref.Dir(), p[1], "package", p[3]),
			func(ctx context.Context, key string) ([]conan.Revision, error) {
				return h.conan.PackageRevisions(ctx, key, ref, p[1], p[3])
			},
		)
	case len(p) == 5 && p[0] == "revisions" && p[2] == "packages" && p[4] == "revisions":
		h.conanRevisions(
			w,
			r,
			rp,
			api,
			path.Join(ref.Dir(), p[1], "package", p[3]),
			func(ctx context.Context, key string) ([]conan.Revision, error) {
				return h.conan.PackageRevisions(ctx, key, ref, p[1], p[3])
			},
		)
	case len(p) == 7 && p[0] == "revisions" && p[2] == "packages" && p[4] == "revisions" && p[6] == "files":
		h.conanFiles(w, r, rp, api, path.Join(ref.Dir(), p[1], "package", p[3], p[5]))
	case len(p) >= 8 && p[0] == "revisions" && p[2] == "packages" && p[4] == "revisions" && p[6] == "files":
		h.conanDownload(w, r, rp, api, path.Join(ref.Dir(), p[1], "package", p[3], p[5], strings.Join(p[7:], "/")))
	default:
		httperror.Write(w, r, http.StatusNotFound, "not found")
	}
}

func (h *Handler) conanLatest(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	api, dir string,
	local func(context.Context, string) ([]conan.Revision, error),
) {
	var all []conan.Revision

	for _, m := range h.conan.Members(r.Context(), rp) {
		if m.Class == "remote" {
			raw, err := h.conan.Upstream(r.Context(), m, api)

			var rev conan.Revision
			if err == nil && json.Unmarshal(raw, &rev) == nil && rev.Revision != "" {
				all = append(all, visibleRevisions(r.Context(), m.Name, dir, []conan.Revision{
					rev,
				})...)
			}

			continue
		}

		revs, err := local(r.Context(), m.Name)
		if h.conanFail(w, r, err) {
			return
		}

		all = append(all, revs...)
	}

	newest, ok := conan.Newest(all)
	if !ok {
		httperror.Write(w, r, http.StatusNotFound, "not found")
		return
	}

	writeJSON(w, http.StatusOK, newest)
}

func (h *Handler) conanRevisions(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	api, dir string,
	local func(context.Context, string) ([]conan.Revision, error),
) {
	var lists [][]conan.Revision

	for _, m := range h.conan.Members(r.Context(), rp) {
		if m.Class == "remote" {
			if raw, err := h.conan.Upstream(r.Context(), m, api); err == nil {
				if revs, err := conan.DecodeRevisions(raw); err == nil {
					lists = append(lists, visibleRevisions(r.Context(), m.Name, dir, revs))
				}
			}

			continue
		}

		revs, err := local(r.Context(), m.Name)
		if h.conanFail(w, r, err) {
			return
		}

		lists = append(lists, revs)
	}

	merged := conan.MergeRevisions(lists...)
	if len(merged) == 0 {
		httperror.Write(w, r, http.StatusNotFound, "not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"revisions": merged,
	})
}

func (h *Handler) conanPackages(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	api string,
	ref conan.Ref,
	rrev string,
) {
	for _, m := range h.conan.Members(r.Context(), rp) {
		if m.Class == "remote" {
			if raw, err := h.conan.Upstream(r.Context(), m, api); err == nil && json.Valid(raw) {
				writeVisibleKeys(w, r, m.Name, path.Join(ref.Dir(), rrev, "package"), raw)
				return
			}

			continue
		}

		pkgs, err := h.conan.Packages(r.Context(), m.Name, ref, rrev)
		if h.conanFail(w, r, err) {
			return
		}

		if len(pkgs) > 0 {
			writeJSON(w, http.StatusOK, pkgs)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{})
}

func (h *Handler) conanFiles(w http.ResponseWriter, r *http.Request, rp repository.Repository, api, dir string) {
	for _, m := range h.conan.Members(r.Context(), rp) {
		if m.Class == "remote" {
			if raw, err := h.conan.Upstream(r.Context(), m, api); err == nil && json.Valid(raw) {
				var doc struct {
					Files map[string]json.RawMessage `json:"files"`
				}
				if !repository.Restricted(r.Context(), m.Name) || json.Unmarshal(raw, &doc) != nil {
					writeBody(w, r, "application/json", raw)
					return
				}

				filterKeys(r.Context(), m.Name, dir, doc.Files)
				writeJSON(w, http.StatusOK, doc)

				return
			}

			continue
		}

		files, err := h.conan.Files(r.Context(), m.Name, dir)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if h.conanFail(w, r, err) {
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"files": files,
		})

		return
	}

	httperror.Write(w, r, http.StatusNotFound, "not found")
}

func (h *Handler) conanDownload(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	api, storePath string,
) {
	for _, m := range h.conan.Members(r.Context(), rp) {
		var (
			item repository.Item
			err  error
		)
		if m.Class == "remote" {
			item, err = h.conan.UpstreamFile(r.Context(), m, api, storePath)
		} else {
			item, err = h.repos.Stat(r.Context(), m.Name, storePath)
		}

		if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrUpstream) {
			continue
		}

		if h.conanFail(w, r, err) {
			return
		}

		if item.IsFolder() || !h.allowDownload(w, r, item) {
			if item.IsFolder() {
				httperror.Write(w, r, http.StatusNotFound, "not found")
			}

			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

		if r.Method != http.MethodHead {
			h.dockerStream(w, r, item)
		}

		return
	}

	httperror.Write(w, r, http.StatusNotFound, "not found")
}

// With X-Checksum-Deploy it only links known content: 201 when stored, 404 so Conan uploads the body.
func (h *Handler) conanUpload(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	ref conan.Ref,
	p []string,
) {
	var storePath string

	switch {
	case len(p) >= 4 && p[0] == "revisions" && p[2] == "files":
		storePath = path.Join(ref.Dir(), p[1], "export", strings.Join(p[3:], "/"))
	case len(p) >= 8 && p[0] == "revisions" && p[2] == "packages" && p[4] == "revisions" && p[6] == "files":
		storePath = path.Join(ref.Dir(), p[1], "package", p[3], p[5], strings.Join(p[7:], "/"))
	default:
		httperror.Write(w, r, http.StatusNotFound, "not found")
		return
	}

	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		target,
		storePath,
		auth.ActionWrite,
	}) {
		return
	}

	in := repository.DeployInput{
		Repository: target,
		Path:       storePath,
		Body:       r.Body,
		Size:       r.ContentLength,
		User:       auth.FromContext(r.Context()).Name(),
		Expected: repository.Checksums{
			SHA1: strings.ToLower(r.Header.Get("X-Checksum-Sha1")),
		},
	}
	if strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true") {
		readable, err := h.CanReadContent(r, in.Expected)
		if h.conanFail(w, r, err) {
			return
		}

		err = repository.ErrChecksumUnknown
		if readable {
			_, err = h.repos.DeployByChecksum(r.Context(), in)
		}

		if errors.Is(err, repository.ErrChecksumUnknown) {
			httperror.Write(w, r, http.StatusNotFound, "checksum not found")
			return
		}

		if h.conanFail(w, r, err) {
			return
		}

		w.WriteHeader(http.StatusCreated)

		return
	}

	if _, err := h.repos.Deploy(r.Context(), in); h.conanFail(w, r, err) {
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// Success is exactly 200.
func (h *Handler) conanDelete(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	ref conan.Ref,
	p []string,
) {
	var target string

	switch {
	case len(p) == 2 && p[0] == "revisions":
		target = path.Join(ref.Dir(), p[1])
	case len(p) == 3 && p[0] == "revisions" && p[2] == "packages":
		target = path.Join(ref.Dir(), p[1], "package")
	case len(p) == 6 && p[0] == "revisions" && p[2] == "packages" && p[4] == "revisions":
		target = path.Join(ref.Dir(), p[1], "package", p[3], p[5])
	default:
		httperror.Write(w, r, http.StatusNotFound, "not found")
		return
	}

	key, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		key,
		target,
		auth.ActionDelete,
	}) {
		return
	}

	if h.conanFail(w, r, h.repos.Delete(r.Context(), key, target)) {
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) conanFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, conan.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		httperror.Write(w, r, http.StatusNotFound, "not found")
	case errors.Is(err, conan.ErrInvalid):
		httperror.Write(w, r, http.StatusBadRequest, err.Error())
	default:
		h.writeError(w, r, err)
	}

	return true
}

func visibleRefs(ctx context.Context, repoKey string, refs []string) []string {
	if !repository.Restricted(ctx, repoKey) {
		return refs
	}

	visible := refs[:0]
	for _, s := range refs {
		nv, uc, _ := strings.Cut(s, "@")
		name, version, _ := strings.Cut(nv, "/")
		user, channel, _ := strings.Cut(uc, "/")

		ref := conan.Ref{
			Name:    name,
			Version: version,
			User:    cmp.Or(user, "_"),
			Channel: cmp.Or(channel, "_"),
		}
		if repository.Visible(ctx, repoKey, ref.Dir()) {
			visible = append(visible, s)
		}
	}

	return visible
}

func visibleRevisions(ctx context.Context, repoKey, dir string, revs []conan.Revision) []conan.Revision {
	if !repository.Restricted(ctx, repoKey) {
		return revs
	}

	visible := revs[:0]
	for _, rev := range revs {
		if repository.Visible(ctx, repoKey, path.Join(dir, rev.Revision)) {
			visible = append(visible, rev)
		}
	}

	return visible
}

func filterKeys(ctx context.Context, repoKey, dir string, m map[string]json.RawMessage) {
	for k := range m {
		if !repository.Visible(ctx, repoKey, path.Join(dir, k)) {
			delete(m, k)
		}
	}
}

func writeVisibleKeys(w http.ResponseWriter, r *http.Request, repoKey, dir string, raw []byte) {
	var doc map[string]json.RawMessage
	if !repository.Restricted(r.Context(), repoKey) || json.Unmarshal(raw, &doc) != nil {
		writeBody(w, r, "application/json", raw)
		return
	}

	filterKeys(r.Context(), repoKey, dir, doc)
	writeJSON(w, http.StatusOK, doc)
}
