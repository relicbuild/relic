package packages

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/nuget"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

const maxNupkgBytes = 2 << 30

func (h *Handler) nugetAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	rest, v3 := strings.CutPrefix(rest, "v3/")
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != nuget.Format {
		httperror.Write(w, r, http.StatusNotFound, "repository not found")
		return
	}

	base := m.base(r) + "/v3/" + repoKey
	switch {
	case r.Method == http.MethodPut && (rest == "" || rest == "package" || rest == "api/v2/package"):
		h.nugetPush(w, r, rp)
	case r.Method == http.MethodDelete:
		idVersion := strings.Split(strings.TrimPrefix(rest, "api/v2/package/"), "/")
		if len(idVersion) != 2 || !nuget.ValidID(idVersion[0]) || !nuget.ValidVersion(idVersion[1]) {
			http.NotFound(w, r)
			return
		}

		h.nugetDelete(w, r, rp, idVersion[0], idVersion[1])
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
	case !v3 && h.nugetV2(w, r, m, rp, rest):
	case rest == "index.json" || rest == "":
		h.nugetIndex(w, base)
	case strings.HasPrefix(rest, "flatcontainer/"):
		h.nugetFlat(w, r, rp, strings.TrimPrefix(rest, "flatcontainer/"))
	case strings.HasPrefix(rest, "registration/"):
		h.nugetRegistration(w, r, rp, base, strings.TrimPrefix(rest, "registration/"))
	case rest == "query":
		h.nugetQuery(w, r, rp, base)
	case strings.HasPrefix(rest, "symbols/"):
		h.nugetSymbol(w, r, rp, strings.TrimPrefix(rest, "symbols/"))
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) nugetIndex(w http.ResponseWriter, base string) {
	type resource struct {
		ID   string `json:"@id"`
		Type string `json:"@type"`
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version": "3.0.0",
		"resources": []resource{
			{
				base + "/flatcontainer/",
				"PackageBaseAddress/3.0.0",
			},
			{
				base + "/registration/",
				"RegistrationsBaseUrl",
			},
			{
				base + "/registration/",
				"RegistrationsBaseUrl/3.6.0",
			},
			{
				base + "/query",
				"SearchQueryService",
			},
			{
				base + "/query",
				"SearchQueryService/3.0.0-beta",
			},
			{
				base + "/query",
				"SearchQueryService/3.0.0-rc",
			},
			{
				base + "/query",
				"SearchQueryService/3.5.0",
			},
			{
				base,
				"PackagePublish/2.0.0",
			},
			{
				base,
				"SymbolPackagePublish/4.9.0",
			},
		},
	})
}

func (h *Handler) nugetFlat(w http.ResponseWriter, r *http.Request, rp repository.Repository, rest string) {
	parts := strings.Split(rest, "/")
	if !nuget.ValidID(parts[0]) || !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		parts[0],
		auth.ActionRead,
	}) {
		if !nuget.ValidID(parts[0]) {
			http.NotFound(w, r)
		}

		return
	}

	id := parts[0]
	switch {
	case len(parts) == 2 && parts[1] == "index.json":
		versions, err := h.nuget.Versions(r.Context(), rp, id)
		if h.nugetFail(w, r, err) {
			return
		}

		writeJSON(w, http.StatusOK, map[string][]string{
			"versions": versions,
		})
	case len(parts) == 3 && nuget.ValidVersion(parts[1]):
		switch strings.ToLower(parts[2]) {
		case strings.ToLower(id + "." + parts[1] + ".nupkg"):
			h.nugetDownload(w, r, rp, id, parts[1])
		case strings.ToLower(id + ".nuspec"):
			item, err := h.nuget.Package(r.Context(), rp, id, parts[1])
			if h.nugetFail(w, r, err) {
				return
			}

			raw, _, err := h.nuget.Nuspec(r.Context(), item)
			if h.nugetFail(w, r, err) {
				return
			}

			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write(raw) //nolint:gosec // an XML document served as such
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) nugetRegistration(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	base, rest string,
) {
	id, file, _ := strings.Cut(rest, "/")
	leaf, _ := strings.CutSuffix(file, ".json")

	if !nuget.ValidID(id) || file != "index.json" && (file == leaf || !nuget.ValidVersion(leaf)) {
		http.NotFound(w, r)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		id,
		auth.ActionRead,
	}) {
		return
	}

	versions, err := h.nuget.Versions(r.Context(), rp, id)
	if h.nugetFail(w, r, err) {
		return
	}

	lower := strings.ToLower(id)

	items := make([]map[string]any, 0, len(versions))
	for _, v := range versions {
		item := map[string]any{
			"@id": base + "/registration/" + lower + "/" + v + ".json",
			"catalogEntry": map[string]any{
				"@id":            base + "/registration/" + lower + "/" + v + ".json",
				"id":             id,
				"version":        v,
				"listed":         true,
				"packageContent": base + "/flatcontainer/" + lower + "/" + v + "/" + lower + "." + v + ".nupkg",
			},
			"packageContent": base + "/flatcontainer/" + lower + "/" + v + "/" + lower + "." + v + ".nupkg",
		}

		if file != "index.json" && strings.EqualFold(v, leaf) {
			item["listed"] = true
			item["registration"] = base + "/registration/" + lower + "/index.json"
			writeJSON(w, http.StatusOK, item)

			return
		}

		items = append(items, item)
	}

	if file != "index.json" {
		http.NotFound(w, r)
		return
	}

	page := map[string]any{
		"@id":   base + "/registration/" + lower + "/index.json#page",
		"count": len(items),
		"items": items,
		"lower": versions[0],
		"upper": versions[len(versions)-1],
	}
	writeJSON(
		w,
		http.StatusOK,
		map[string]any{
			"@id":   base + "/registration/" + lower + "/index.json",
			"count": 1,
			"items": []any{
				page,
			},
		},
	)
}

func (h *Handler) nugetQuery(w http.ResponseWriter, r *http.Request, rp repository.Repository, base string) {
	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}) {
		return
	}

	q := r.URL.Query()
	skip, _ := strconv.Atoi(q.Get("skip"))

	take, _ := strconv.Atoi(q.Get("take"))
	if take <= 0 || take > 1000 {
		take = 20
	}

	results, total, err := h.nuget.Search(r.Context(), rp, q.Get("q"), max(skip, 0), take)
	if h.nugetFail(w, r, err) {
		return
	}

	data := make([]map[string]any, 0, len(results))
	for _, res := range results {
		versions := make([]map[string]string, 0, len(res.Versions))
		for _, v := range res.Versions {
			versions = append(
				versions,
				map[string]string{
					"version": v,
					"@id":     base + "/registration/" + strings.ToLower(res.ID) + "/" + v + ".json",
				},
			)
		}

		data = append(data, map[string]any{
			"@id":      base + "/registration/" + strings.ToLower(res.ID) + "/index.json",
			"id":       res.ID,
			"version":  res.Versions[len(res.Versions)-1],
			"versions": versions,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totalHits": total,
		"data":      data,
	})
}

func (h *Handler) nugetPush(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// The package id, which the permission check needs, is inside the
	// package; refuse callers who may write nowhere in the repository
	// before spooling it.
	if ok, err := h.CanRepoAnywhere(r, target, auth.ActionWrite); err != nil || !ok {
		if err != nil {
			h.writeError(w, r, err)
		} else {
			Forbidden(w, r, string(auth.ActionWrite), target)
		}

		return
	}

	mr, err := r.MultipartReader()
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "expected a multipart upload")
		return
	}

	part, err := mr.NextPart()
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "missing package")
		return
	}

	tmp, err := os.CreateTemp("", "relic-nupkg-")
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	n, err := io.Copy(tmp, io.LimitReader(part, maxNupkgBytes+1))
	if err != nil || n > maxNupkgBytes {
		httperror.Write(w, r, http.StatusBadRequest, "package unreadable or too large")
		return
	}

	info, err := tmp.Stat()
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	spec, _, err := nuget.ReadNuspec(tmp, info.Size())
	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, err.Error())
		return
	}

	stored := nuget.Path(spec.Metadata.ID, spec.Metadata.Version)
	if spec.SymbolsPackage() {
		stored = nuget.SymbolsPath(spec.Metadata.ID, spec.Metadata.Version)
	}

	if !h.RequireRepo(w, r, RepoCheck{
		target,
		stored,
		auth.ActionWrite,
	}) {
		return
	}

	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		h.writeError(w, r, err)
		return
	}

	if _, err := h.nuget.Push(r.Context(), target, tmp, auth.FromContext(r.Context()).Name()); err != nil {
		switch {
		case errors.Is(err, nuget.ErrInvalid):
			httperror.Write(w, r, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, nuget.ErrExists):
			httperror.Write(w, r, http.StatusConflict, err.Error())
			return
		}

		h.writeError(w, r, err)

		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) nugetSymbol(w http.ResponseWriter, r *http.Request, rp repository.Repository, rest string) {
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}

	item, body, err := h.nuget.Symbol(r.Context(), rp, parts[0]+"/"+parts[1], parts[2])
	if h.nugetFail(w, r, err) || !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		item.Path(),
		auth.ActionRead,
	}) || !h.allowDownload(w, r, item) {
		return
	}

	writeBody(w, r, "application/octet-stream", body)
}

func (h *Handler) nugetDownload(w http.ResponseWriter, r *http.Request, rp repository.Repository, id, version string) {
	item, err := h.nuget.Package(r.Context(), rp, id, version)
	if h.nugetFail(w, r, err) || !h.allowDownload(w, r, item) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

	if r.Method != http.MethodHead {
		h.dockerStream(w, r, item)
	}
}

func (h *Handler) nugetDelete(w http.ResponseWriter, r *http.Request, rp repository.Repository, id, version string) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	trp, err := h.repos.GetRepository(r.Context(), target)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	item, err := h.nuget.Package(r.Context(), trp, id, version)
	if h.nugetFail(w, r, err) {
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		item.Repository,
		item.Path(),
		auth.ActionDelete,
	}) {
		return
	}

	if err := h.repos.DeleteToTrash(
		r.Context(),
		item.Repository,
		item.Path(),
		auth.FromContext(r.Context()).Name(),
	); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) nugetFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, nuget.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		http.NotFound(w, r)
	default:
		h.writeError(w, r, err)
	}

	return true
}
