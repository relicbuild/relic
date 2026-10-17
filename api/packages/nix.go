package packages

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/nix"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

func (h *Handler) nixAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, p, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != nix.Format {
		httperror.Write(w, r, http.StatusNotFound, "No Nix repository "+repoKey)
		return
	}

	switch r.Method {
	case http.MethodPut:
		h.nixPut(w, r, rp, p)
	case http.MethodGet, http.MethodHead:
		h.nixGet(w, r, rp, p)
	default:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) nixGet(w http.ResponseWriter, r *http.Request, rp repository.Repository, p string) {
	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		"",
		auth.ActionRead,
	}) {
		return
	}

	if p == "nix-cache-info" {
		w.Header().Set("Content-Type", "text/x-nix-cache-info")

		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, nix.CacheInfo)
		}

		return
	}

	item, err := h.nix.Resolve(r.Context(), rp, p)
	if errors.Is(err, repository.ErrNotFound) || errors.Is(err, repository.ErrInvalidPath) {
		httperror.Write(w, r, http.StatusNotFound, "not found")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		item.Repository,
		item.Path(),
		auth.ActionRead,
	}) || !h.allowDownload(w, r, item) {
		return
	}

	switch {
	case strings.HasSuffix(p, ".narinfo"):
		w.Header().Set("Content-Type", "text/x-nix-narinfo")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))

	if r.Method == http.MethodGet {
		h.dockerStream(w, r, item)
	}
}

func (h *Handler) nixPut(w http.ResponseWriter, r *http.Request, rp repository.Repository, p string) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	dest, err := h.repos.GetRepository(r.Context(), target)
	if err != nil || dest.Class != "local" || dest.Format != nix.Format {
		httperror.Write(
			w,
			r,
			http.StatusBadRequest,
			"nix copy needs a local Nix repository (or a virtual one that deploys to one)",
		)

		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		dest.Name,
		p,
		auth.ActionWrite,
	}) {
		return
	}

	err = h.nix.Put(r.Context(), dest, p, r.Body, auth.FromContext(r.Context()).Name())
	if errors.Is(err, nix.ErrInvalid) {
		httperror.Write(w, r, http.StatusBadRequest, err.Error())
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusCreated)
}
