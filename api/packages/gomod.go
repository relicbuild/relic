package packages

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/gomod"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

func (h *Handler) goAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	if r.Method == http.MethodPut {
		m.Files(w, r, strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix))

		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)

	repoKey, rest, _ := strings.Cut(rest, "/")
	if sumdb, ok := strings.CutPrefix(rest, "sumdb/"); ok {
		h.goSumDB(w, r, repoKey, sumdb)
		return
	}

	mod, tail, ok := splitGoPath(rest)
	if !ok || !gomod.ValidModule(mod) {
		http.NotFound(w, r)
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil && !errors.Is(err, repository.ErrRepositoryNotFound) {
		h.writeError(w, r, err)
		return
	}

	if err != nil || rp.Format != gomod.Format {
		http.NotFound(w, r)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		repoKey,
		mod,
		auth.ActionRead,
	}) {
		return
	}

	switch tail {
	case "@latest":
		info, err := h.gomod.Latest(r.Context(), rp, mod)
		h.goReply(w, r, err, func() {
			writeGoInfo(w, info)
		})
	case "@v/list":
		versions, err := h.gomod.List(r.Context(), rp, mod)
		h.goReply(w, r, err, func() {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")

			body := strings.Join(versions, "\n")
			if body != "" {
				body += "\n"
			}

			_, _ = w.Write([]byte(body)) //nolint:gosec // text/plain list of validated versions
		})
	default:
		name := strings.TrimPrefix(tail, "@v/")

		version, ext := splitVersionFile(name)
		if version == "latest" && ext == ".info" {
			info, err := h.gomod.Latest(r.Context(), rp, mod)
			h.goReply(w, r, err, func() {
				writeGoInfo(w, info)
			})

			return
		}

		if !gomod.ValidVersion(version) {
			http.NotFound(w, r)
			return
		}

		if ext == ".info" {
			info, err := h.gomod.Info(r.Context(), rp, mod, version)
			h.goReply(w, r, err, func() {
				writeGoInfo(w, info)
			})

			return
		}

		item, err := h.gomod.File(r.Context(), rp, mod, name)
		h.goReply(w, r, err, func() {
			ct := "application/zip"
			if ext == ".mod" {
				ct = "text/plain; charset=utf-8"
			}

			if !h.allowDownload(w, r, item) {
				return
			}

			w.Header().Set("Content-Type", ct)
			w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

			if r.Method == http.MethodHead {
				return
			}

			h.dockerStream(w, r, item)
		})
	}
}

func splitGoPath(rest string) (mod, tail string, ok bool) {
	if m, found := strings.CutSuffix(rest, "/@latest"); found {
		return m, "@latest", true
	}

	i := strings.LastIndex(rest, "/@v/")
	if i <= 0 || strings.Contains(rest[i+4:], "/") || rest[i+4:] == "" {
		return "", "", false
	}

	return rest[:i], rest[i+1:], true
}

func splitVersionFile(name string) (version, ext string) {
	for _, e := range []string{
		".info",
		".mod",
		".zip",
	} {
		if v, ok := strings.CutSuffix(name, e); ok {
			return v, e
		}
	}

	return "", ""
}

func writeGoInfo(w http.ResponseWriter, info gomod.Info) {
	writeJSON(
		w,
		http.StatusOK,
		map[string]string{
			"Version": info.Version,
			"Time":    info.Time.UTC().Format(time.RFC3339),
		},
	)
}

func (h *Handler) goReply(w http.ResponseWriter, r *http.Request, err error, ok func()) {
	switch {
	case err == nil:
		ok()
	case errors.Is(err, gomod.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		h.writeError(w, r, err)
	}
}

func (h *Handler) goSumDB(w http.ResponseWriter, r *http.Request, repoKey, rest string) {
	name, p, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil && !errors.Is(err, repository.ErrRepositoryNotFound) {
		h.writeError(w, r, err)
		return
	}

	if err != nil || rp.Format != gomod.Format {
		http.NotFound(w, r)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		repoKey,
		"",
		auth.ActionRead,
	}) {
		return
	}

	remote, ok := h.gomod.SumDBRemote(r.Context(), rp, name)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if p == "supported" {
		w.WriteHeader(http.StatusOK)
		return
	}

	item, body, err := h.gomod.SumDB(r.Context(), remote, name, p)
	h.goReply(w, r, err, func() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")

		if body != nil {
			w.Header().Set("Cache-Control", "no-cache")
			_, _ = w.Write(body) //nolint:gosec // G705: the signed note, served as text/plain

			return
		}

		if strings.HasPrefix(p, "tile/") {
			w.Header().Set("Content-Type", "application/octet-stream")
		}

		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

		if r.Method != http.MethodHead {
			h.dockerStream(w, r, item)
		}
	})
}
