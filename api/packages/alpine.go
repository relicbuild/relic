package packages

import (
	"errors"
	"net/http"
	"path"
	"strconv"

	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/repository"
)

func (h *Handler) apkIndex(w http.ResponseWriter, r *http.Request, rp repository.Repository, dir string) {
	body, err := h.alpine.Index(r.Context(), rp, dir)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))

	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(body) //nolint:gosec // G705: a gzip index served as application/gzip with nosniff
}

func (h *Handler) isAPKIndex(r *http.Request, t Target) (repository.Repository, bool, error) {
	if h.alpine == nil || path.Base(t.Path) != alpine.IndexFile {
		return repository.Repository{}, false, nil
	}

	rp, err := h.repos.GetRepository(r.Context(), t.Repo)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return repository.Repository{}, false, nil
	}

	if err != nil {
		return repository.Repository{}, false, err
	}

	return rp, rp.Format == alpine.Format && rp.Class != "remote", nil
}
