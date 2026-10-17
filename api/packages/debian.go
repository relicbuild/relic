package packages

import (
	"net/http"
	"strings"

	"relic/internal/artifacts/formats/debian"
	"relic/internal/artifacts/repository"
)

func (h *Handler) debianIndex(w http.ResponseWriter, r *http.Request, t Target) bool {
	rest, ok := strings.CutPrefix(t.Path, "dists/")
	if h.debian == nil || !ok {
		return false
	}

	rp, err := h.repos.GetRepository(r.Context(), t.Repo)
	if err != nil || rp.Format != debian.Format || rp.Class == "remote" {
		return false
	}

	dist, file, _ := strings.Cut(rest, "/")

	d, err := h.debian.Distribution(r.Context(), rp, dist)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}

	var body []byte

	contentType := "text/plain; charset=utf-8"

	switch file {
	case "Release":
		body = d.Release
	case "InRelease", "Release.gpg":
		if body, err = h.debian.Sign(r.Context(), rp, d.Release, file == "Release.gpg"); err != nil {
			h.writeError(w, r, err)
			return true
		}
	default:
		var found bool
		if body, found = d.Files[file]; !found {
			h.writeError(w, r, repository.ErrNotFound)
			return true
		}

		if strings.HasSuffix(file, ".gz") {
			contentType = "application/gzip"
		}
	}

	w.Header().Set("Content-Type", contentType)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}

	return true
}
