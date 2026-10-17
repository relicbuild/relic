package packages

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"

	"golang.org/x/mod/semver"
)

func terraformModules(m Mount) string { return m.Prefix + "v1/modules/" }

func terraformArchives(m Mount) string { return m.Prefix + "archive/" }

const archiveTTL = 5 * time.Minute

func (h *Handler) terraformDiscovery(w http.ResponseWriter, _ *http.Request, m Mount) {
	writeJSON(w, http.StatusOK, map[string]string{
		"modules.v1": terraformModules(m),
	})
}

func (h *Handler) terraformModule(w http.ResponseWriter, r *http.Request, m Mount) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, terraformModules(m)), "/")
	if len(parts) < 4 {
		http.NotFound(w, r)
		return
	}

	if !strings.Contains(parts[0], "__") {
		httperror.Write(w, r, http.StatusNotFound, "use <repository>__<namespace> as the module namespace")
		return
	}

	rp, namespace, err := h.terraformRepository(r, parts[0])
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	repoKey := rp.Name
	folder := namespace + "/" + parts[1] + "/" + parts[2]

	if !h.RequireRepo(w, r, RepoCheck{
		repoKey,
		folder,
		auth.ActionRead,
	}) {
		return
	}

	versions, err := h.terraformVersions(r, rp, folder)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	switch {
	case len(parts) == 4 && parts[3] == "versions":
		list := make([]map[string]string, 0, len(versions))
		for v := range versions {
			list = append(list, map[string]string{
				"version": v,
			})
		}

		slices.SortFunc(
			list,
			func(a, b map[string]string) int {
				return semver.Compare("v"+a["version"], "v"+b["version"])
			},
		)
		writeJSON(w, http.StatusOK, map[string]any{
			"modules": []any{
				map[string]any{
					"versions": list,
				},
			},
		})
	case len(parts) == 5 && parts[4] == "download":
		item, ok := versions[strings.TrimPrefix(parts[3], "v")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// terraform fetches the archive without its token, depending on its version.
		p := terraformArchives(m) + item.Repository + "/" + escapePath(item.Path())
		sig := h.tokens.SignPath(p, auth.FromContext(r.Context()).Name(), archiveTTL)
		w.Header().Set("X-Terraform-Get", p+"?archive=zip&"+sig)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) terraformRepository(
	r *http.Request,
	segment string,
) (repository.Repository, string, error) {
	for start := 0; ; {
		i := strings.Index(segment[start:], "__")
		if i < 0 {
			return repository.Repository{}, "", repository.ErrRepositoryNotFound
		}

		key, namespace := segment[:start+i], segment[start+i+2:]
		start += i + 1

		if key == "" || namespace == "" {
			continue
		}

		rp, err := h.repos.GetRepository(r.Context(), key)
		if !errors.Is(err, repository.ErrRepositoryNotFound) {
			return rp, namespace, err
		}
	}
}

func (h *Handler) terraformVersions(
	r *http.Request,
	rp repository.Repository,
	folder string,
) (map[string]repository.Item, error) {
	members := h.ReadRepos(r.Context(), rp)

	keys := make([]string, 0, len(members))
	for _, m := range members {
		keys = append(keys, m.Name)
	}

	out := map[string]repository.Item{}

	for _, key := range keys {
		dir, err := h.repos.Stat(r.Context(), key, folder)
		if errors.Is(err, repository.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		kids, err := h.repos.Children(r.Context(), dir)
		if err != nil {
			return nil, err
		}

		for _, k := range kids {
			if tag, ok := strings.CutSuffix(k.Name, ".zip"); ok && !k.IsFolder() {
				v := strings.TrimPrefix(tag, "v")
				if _, dup := out[v]; !dup && semver.IsValid("v"+v) {
					out[v] = k
				}
			}
		}
	}

	return out, nil
}

func (h *Handler) terraformArchive(w http.ResponseWriter, r *http.Request, m Mount) {
	q := r.URL.Query()

	user, err := h.tokens.VerifyPath(
		r.URL.EscapedPath(),
		q.Get("user"),
		q.Get("exp"),
		q.Get("kid"),
		q.Get("sig"),
	)
	if err != nil {
		httperror.Write(w, r, http.StatusForbidden, "invalid or expired download link")
		return
	}

	if auth.FromContext(r.Context()).Anonymous() {
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{
			Username: user,
		}))
	}

	rest := strings.TrimPrefix(r.URL.Path, terraformArchives(m))
	repoKey, p, _ := strings.Cut(rest, "/")

	item, err := h.repos.Stat(r.Context(), repoKey, p)
	if err != nil || item.IsFolder() {
		http.NotFound(w, r)
		return
	}

	if !h.allowDownload(w, r, item) {
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
	h.dockerStream(w, r, item)
}
