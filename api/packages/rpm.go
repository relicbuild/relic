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

	if file == rpm.RepoMDKey {
		key, err := h.rpm.PublicKey(r.Context(), rp)
		if err != nil {
			h.writeError(w, r, err)
			return true
		}

		writeRepodataFile(w, r, "application/pgp-keys", []byte(key))

		return true
	}

	files, err := h.rpm.Repodata(r.Context(), rp, root)
	if err != nil {
		h.writeError(w, r, err)
		return true
	}

	var (
		body        []byte
		contentType string
	)

	switch file {
	case rpm.RepoMD:
		body = files[rpm.RepoMD]

		contentType = "text/xml; charset=utf-8"
	case rpm.RepoMDSignature:
		if body, err = h.rpm.Sign(r.Context(), rp, files[rpm.RepoMD]); err != nil {
			h.writeError(w, r, err)
			return true
		}

		contentType = "application/pgp-signature"
	default:
		var found bool
		if body, found = files[file]; !found {
			h.writeError(w, r, repository.ErrNotFound)
			return true
		}

		contentType = "application/gzip"
	}

	writeRepodataFile(w, r, contentType, body)

	return true
}

func writeRepodataFile(w http.ResponseWriter, r *http.Request, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))

	if r.Method != http.MethodHead {
		_, _ = w.Write(body) //nolint:gosec // G705: generated repository metadata, served with its own content type
	}
}
