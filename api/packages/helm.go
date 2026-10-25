package packages

import (
	"net/http"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/helm"
	"relic/internal/artifacts/repository"
)

func (h *Handler) helmIndex(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	body, err := h.helm.BuildIndex(r.Context(), rp)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/x-yaml")

	if r.Method == http.MethodHead {
		return
	}

	_, _ = w.Write(body)
}

func (h *Handler) helmForeignChart(w http.ResponseWriter, r *http.Request, m Mount, t Target) bool {
	if h.helm == nil || !strings.HasPrefix(t.Path, helm.ForeignPrefix) {
		return false
	}

	rp, err := h.repos.GetRepository(r.Context(), t.Repo)
	if err != nil || rp.Format != helm.Format {
		return false
	}

	item, err := h.helm.ForeignChart(r.Context(), rp, t.Path)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}

	if item.Repository != t.Repo && !h.RequireRepo(w, r, RepoCheck{
		item.Repository,
		item.Path(),
		auth.ActionRead,
	}) {
		return true
	}

	h.serveItem(w, r, m, item)

	return true
}

func (h *Handler) helmAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	m.Files(w, r, strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix))
}
