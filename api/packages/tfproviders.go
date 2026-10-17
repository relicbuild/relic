package packages

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/terraform"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

func (h *Handler) terraformProviders(w http.ResponseWriter, r *http.Request, m Mount) {
	repoKey, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, m.Prefix), "/")
	rest, ok := strings.CutPrefix(rest, "providers/")

	parts := strings.Split(rest, "/")
	if !ok || len(parts) != 4 || h.tfProviders == nil {
		http.NotFound(w, r)
		return
	}

	a, valid := terraform.ParseAddress(parts[0], parts[1], parts[2])
	if !valid {
		http.NotFound(w, r)
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != repository.FormatTerraform {
		httperror.Write(w, r, http.StatusNotFound, "repository not found")
		return
	}

	file := parts[3]
	if !h.RequireRepo(
		w,
		r,
		RepoCheck{
			repoKey,
			a.Hostname + "/" + a.Namespace + "/" + a.Type + "/" + file,
			auth.ActionRead,
		},
	) {
		return
	}

	ctx := r.Context()

	switch {
	case file == "index.json":
		versions, err := h.tfProviders.Versions(ctx, rp, a)
		if h.tfFail(w, r, err) {
			return
		}

		doc := map[string]struct{}{}
		for _, v := range versions {
			doc[v] = struct{}{}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"versions": doc,
		})
	case strings.HasSuffix(file, ".json"):
		archives, err := h.tfProviders.Archives(ctx, rp, a, strings.TrimSuffix(file, ".json"))
		if h.tfFail(w, r, err) {
			return
		}

		type entry struct {
			URL    string   `json:"url"`
			Hashes []string `json:"hashes"`
		}

		doc := map[string]entry{}
		user := auth.FromContext(ctx).Name()

		for platform, ar := range archives {
			// terraform fetches archives without the host's credentials.
			p := providerArchives(m) + repoKey + "/" + escapePath(a.Hostname+"/"+a.Namespace+"/"+a.Type+"/"+ar.Filename)
			doc[platform] = entry{
				URL: p + "?" + h.tokens.SignPath(p, user, archiveTTL),
				Hashes: []string{
					ar.Hash,
				},
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"archives": doc,
		})
	case strings.HasSuffix(file, ".zip"):
		item, err := h.tfProviders.Package(ctx, rp, a, file)
		if h.tfFail(w, r, err) || !h.allowDownload(w, r, item) {
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

		if r.Method != http.MethodHead {
			h.dockerStream(w, r, item)
		}
	default:
		http.NotFound(w, r)
	}
}

func providerArchives(m Mount) string { return m.Prefix + "provider-archive/" }

func (h *Handler) terraformProviderArchive(w http.ResponseWriter, r *http.Request, m Mount) {
	q := r.URL.Query()

	user, err := h.tokens.VerifyPath(
		r.URL.EscapedPath(),
		q.Get("user"),
		q.Get("exp"),
		q.Get("kid"),
		q.Get("sig"),
	)
	if err != nil || h.tfProviders == nil {
		httperror.Write(w, r, http.StatusForbidden, "invalid or expired download link")
		return
	}

	if auth.FromContext(r.Context()).Anonymous() {
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
			Username: user,
		}))
	}

	parts := strings.Split(strings.TrimPrefix(r.URL.Path, providerArchives(m)), "/")
	if len(parts) != 5 {
		http.NotFound(w, r)
		return
	}

	a, ok := terraform.ParseAddress(parts[1], parts[2], parts[3])

	rp, err := h.repos.GetRepository(r.Context(), parts[0])
	if !ok || err != nil {
		http.NotFound(w, r)
		return
	}

	item, err := h.tfProviders.Package(r.Context(), rp, a, parts[4])
	if h.tfFail(w, r, err) || !h.allowDownload(w, r, item) {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

	if r.Method != http.MethodHead {
		h.dockerStream(w, r, item)
	}
}

func (h *Handler) tfFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, terraform.ErrNotFound):
		httperror.Write(w, r, http.StatusNotFound, "provider not found")
	default:
		h.writeError(w, r, err)
	}

	return true
}
