package packages

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"relic/internal/artifacts/formats/maven"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

var checksumKinds = map[string]func(repository.Checksums) string{
	".sha1": func(c repository.Checksums) string {
		return c.SHA1
	},
	".md5": func(c repository.Checksums) string {
		return c.MD5
	},
	".sha256": func(c repository.Checksums) string {
		return c.SHA256
	},
}

func checksumOf(p string) (base string, get func(repository.Checksums) string, ok bool) {
	for suffix, get := range checksumKinds {
		if b, found := strings.CutSuffix(p, suffix); found {
			return b, get, true
		}
	}

	return "", nil, false
}

// IsChecksumName reports whether p names a checksum file (.sha1, .md5, …).
func IsChecksumName(p string) bool {
	_, _, ok := checksumOf(p)
	return ok
}

func (h *Handler) checksumFile(w http.ResponseWriter, r *http.Request, t Target) bool {
	base, get, ok := checksumOf(t.Path)
	if !ok {
		return false
	}

	item, err := h.repos.Resolve(r.Context(), t.Repo, base)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.writeError(w, r, err)
		return true
	}

	if err != nil || item.IsFolder() {
		return false
	}

	sum := get(item.Checksums)

	w.Header().Set("Content-Type", "application/x-checksum")
	w.Header().Set("Content-Length", strconv.Itoa(len(sum)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, sum)
	}

	return true
}

// VerifyChecksumUpload handles a checksum file PUT for an existing
// artifact: the first whitespace-separated token must match.
func (h *Handler) VerifyChecksumUpload(w http.ResponseWriter, r *http.Request, t Target) (ok, handled bool) {
	base, get, _ := checksumOf(t.Path)
	if strings.HasSuffix(base, "/"+maven.MetadataFile) || base == maven.MetadataFile {
		// The server recalculates Maven metadata, so the client's checksum
		// never matches; accept and ignore it.
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 1024))
		return true, true
	}

	item, err := h.repos.Stat(r.Context(), t.Repo, base)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.writeError(w, r, err)
		return false, true
	}

	if err != nil || item.IsFolder() {
		return false, false
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1024))
	if err != nil {
		h.writeError(w, r, err)
		return false, true
	}

	fields := strings.Fields(string(body))
	if len(fields) == 0 || !strings.EqualFold(fields[0], get(item.Checksums)) {
		httperror.Write(w, r, http.StatusConflict, "Checksum error: the uploaded checksum does not match "+base)
		return false, true
	}

	return true, true
}

func (h *Handler) mergedMetadata(w http.ResponseWriter, r *http.Request, t Target) {
	body, err := h.maven.MergedMetadata(r.Context(), t.Repo, t.Path)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	sums := repository.ChecksumsOf(body)
	w.Header().Set("X-Checksum-Sha1", sums.SHA1)
	w.Header().Set("X-Checksum-Md5", sums.MD5)
	w.Header().Set("X-Checksum-Sha256", sums.SHA256)
	w.Header().Set("Content-Type", "application/xml")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)

	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

func (h *Handler) mergedMetadataChecksum(
	w http.ResponseWriter,
	r *http.Request,
	t Target,
	base string,
	get func(repository.Checksums) string,
) {
	body, err := h.maven.MergedMetadata(r.Context(), t.Repo, base)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeBody(w, r, "text/plain; charset=utf-8", []byte(get(repository.ChecksumsOf(body))))
}
