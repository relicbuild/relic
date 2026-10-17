package packages

import (
	"net/http"
	"strings"

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

func (h *Handler) helmAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	m.Files(w, r, strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix))
}
