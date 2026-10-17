package packages

import (
	"net/http"
	"path"
	"strconv"
	"strings"

	"relic/internal/artifacts/formats/rpm"
	"relic/internal/artifacts/repository"
)

func (h *Handler) rpmRepodata(w http.ResponseWriter, r *http.Request, t Target) bool {
	folder, file := path.Split(t.Path)
	folder = strings.TrimSuffix(folder, "/")

	if h.rpm == nil || path.Base(folder) != rpm.RepodataFolder {
		return false
	}

	root := path.Dir(folder)
	if root == "." {
		root = ""
	}

	rp, err := h.repos.GetRepository(r.Context(), t.Repo)
	if err != nil || !rpm.ServesRepodata(rp, root) {
		return false
	}

	files, err := h.rpm.Repodata(r.Context(), rp, root)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}

	var body []byte

	switch file {
	case rpm.RepoMD:
		body = files[rpm.RepoMD]

		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	case rpm.RepoMDSignature:
		if body, err = h.rpm.Sign(r.Context(), rp, files[rpm.RepoMD]); err != nil {
			h.writeError(w, r, err)
			return true
		}

		w.Header().Set("Content-Type", "application/pgp-signature")
	case rpm.RepoMDKey:
		key, err := h.rpm.PublicKey(r.Context(), rp)
		if err != nil {
			h.writeError(w, r, err)
			return true
		}

		body = []byte(key)

		w.Header().Set("Content-Type", "application/pgp-keys")
	default:
		var found bool
		if body, found = files[file]; !found {
			h.writeError(w, r, repository.ErrNotFound)
			return true
		}

		w.Header().Set("Content-Type", "application/gzip")
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))

	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // G705: generated repository metadata, served with its own content type
	}

	return true
}
