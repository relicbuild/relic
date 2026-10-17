package packages

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/cargo"
	"relic/internal/artifacts/repository"
)

func (h *Handler) cargoAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != cargo.Format {
		cargoError(w, http.StatusNotFound, "repository not found")
		return
	}

	parts := strings.Split(rest, "/")
	switch {
	case r.Method == http.MethodPut && rest == "api/v1/crates/new":
		h.cargoPublish(w, r, rp)
	case len(parts) == 6 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "crates" &&
		(r.Method == http.MethodDelete && parts[5] == "yank" || r.Method == http.MethodPut && parts[5] == "unyank"):
		h.cargoYank(w, r, rp, parts[3], parts[4], parts[5] == "yank")
	case r.Method != http.MethodGet && r.Method != http.MethodHead:
		cargoError(w, http.StatusMethodNotAllowed, "method not allowed")
	case !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}):
	case rest == "index/config.json":
		base := m.base(r) + "/" + rp.Name
		writeJSON(w, http.StatusOK, map[string]any{
			"dl":            base + "/v1/crates",
			"api":           base,
			"auth-required": true,
		})
	case strings.HasPrefix(rest, "index/"):
		h.cargoIndex(w, r, rp, strings.TrimPrefix(rest, "index/"))
	case len(parts) == 5 && parts[0] == "v1" && parts[1] == "crates" && parts[4] == "download":
		item, err := h.cargo.Crate(r.Context(), rp, parts[2], parts[3])
		if h.cargoFail(w, r, err) || !h.allowDownload(w, r, item) {
			return
		}

		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

		if r.Method != http.MethodHead {
			h.dockerStream(w, r, item)
		}
	case rest == "api/v1/crates":
		limit, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if limit <= 0 || limit > 100 {
			limit = 10
		}

		hits, total, err := h.cargo.SearchRepositories(r.Context(), rp, r.URL.Query().Get("q"), limit)
		if h.cargoFail(w, r, err) {
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"crates": hits,
			"meta": map[string]int{
				"total": total,
			},
		})
	default:
		cargoError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) cargoIndex(w http.ResponseWriter, r *http.Request, rp repository.Repository, p string) {
	name := p[strings.LastIndexByte(p, '/')+1:]
	if cargo.IndexPrefix(name)+strings.ToLower(name) != p {
		cargoError(w, http.StatusNotFound, "not found")
		return
	}

	body, err := h.cargo.Index(r.Context(), rp, name)
	if h.cargoFail(w, r, err) {
		return
	}

	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	w.Header().Set("ETag", etag)

	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	writeBody(w, r, "text/plain; charset=utf-8", body)
}

func (h *Handler) cargoTarget(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
) (repository.Repository, bool) {
	if rp.Class == "virtual" {
		key, err := h.repos.DeployTarget(r.Context(), rp.Name)
		if err != nil {
			h.writeError(w, r, err)
			return repository.Repository{}, false
		}

		if rp, err = h.repos.GetRepository(r.Context(), key); err != nil {
			h.writeError(w, r, err)
			return repository.Repository{}, false
		}
	}

	if rp.Class != "local" {
		cargoError(
			w,
			http.StatusBadRequest,
			"publish needs a local Cargo repository (or a virtual with a default deployment repository)",
		)

		return repository.Repository{}, false
	}

	return rp, true
}

func (h *Handler) cargoPublish(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	rp, ok := h.cargoTarget(w, r, rp)
	if !ok {
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"crates",
		auth.ActionWrite,
	}) {
		return
	}

	release := reserveBuffer(w, r, cargo.MaxPublish, 1)
	if release == nil {
		return
	}
	defer release()

	body, err := io.ReadAll(io.LimitReader(r.Body, cargo.MaxPublish+1))
	if err != nil || len(body) > cargo.MaxPublish {
		cargoError(w, http.StatusBadRequest, "publish body unreadable or too large")
		return
	}

	if _, err := h.cargo.Publish(r.Context(), rp.Name, body, auth.FromContext(r.Context()).Name()); h.cargoFail(
		w,
		r,
		err,
	) {
		return
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]any{
			"warnings": map[string][]string{
				"invalid_categories": {},
				"invalid_badges":     {},
				"other":              {},
			},
		},
	)
}

func (h *Handler) cargoYank(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	name, version string,
	yank bool,
) {
	rp, ok := h.cargoTarget(w, r, rp)
	if !ok {
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"crates",
		auth.ActionWrite,
	}) {
		return
	}

	if h.cargoFail(w, r, h.cargo.SetYanked(r.Context(), rp.Name, name, version, yank)) {
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}

func cargoError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]any{
		"errors": []map[string]string{
			{
				"detail": detail,
			},
		},
	})
}

func (h *Handler) cargoFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, cargo.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		cargoError(w, http.StatusNotFound, "not found")
	case errors.Is(err, cargo.ErrExists):
		cargoError(w, http.StatusConflict, err.Error())
	case errors.Is(err, cargo.ErrInvalid), errors.Is(err, repository.ErrChecksumMismatch):
		cargoError(w, http.StatusBadRequest, err.Error())
	default:
		h.writeError(w, r, err)
	}

	return true
}
