package packages

import (
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/alpine"
	"relic/internal/artifacts/formats/helm"
	"relic/internal/artifacts/formats/rpm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
	"relic/internal/platform/objectstore"
)

// Target is a file or folder in the raw repository layout:
// <mount>/<repo>/<path>.
type Target struct {
	Repo   string
	Path   string
	Dir    bool
	Props  repository.Properties
	Cached bool
}

// ServeFile answers a GET or HEAD of t in the raw repository layout: the
// file, a folder listing, or an index or metadata file the repository's
// format generates. The caller has checked read permission on t.
func (h *Handler) ServeFile(w http.ResponseWriter, r *http.Request, m Mount, t Target) {
	ctx := r.Context()
	if t.Cached {
		item, err := h.repos.Stat(ctx, t.Repo, t.Path)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		h.serveItem(w, r, m, item)

		return
	}

	if h.maven != nil && h.maven.ServesMergedMetadata(ctx, t.Repo, t.Path) {
		h.mergedMetadata(w, r, t)
		return
	}

	if base, get, ok := checksumOf(t.Path); ok && h.maven != nil && h.maven.ServesMergedMetadata(ctx, t.Repo, base) {
		h.mergedMetadataChecksum(w, r, t, base, get)
		return
	}

	if t.Path == helm.IndexFile || strings.HasPrefix(t.Path, "dists/") || path.Base(t.Path) == alpine.IndexFile ||
		path.Base(path.Dir(t.Path)) == rpm.RepodataFolder {
		filtered, err := h.visibility(ctx)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		r, ctx = r.WithContext(filtered), filtered
	}

	if t.Path == helm.IndexFile && h.helm != nil {
		if rp, err := h.repos.GetRepository(ctx, t.Repo); err == nil && rp.Format == helm.Format {
			h.helmIndex(w, r, rp)
			return
		}
	}

	if h.debianIndex(w, r, t) {
		return
	}

	if h.rpmRepodata(w, r, t) {
		return
	}

	apkRepo, isIndex, err := h.isAPKIndex(r, t)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if isIndex {
		h.apkIndex(w, r, apkRepo, path.Dir(t.Path))
		return
	}

	item, err := h.repos.Resolve(ctx, t.Repo, t.Path)
	if errors.Is(err, repository.ErrNotFound) {
		if h.checksumFile(w, r, t) {
			return
		}

		if gen, body, ok := h.GeneratedFile(r, t.Repo, t.Path); ok {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Checksum-Sha256", gen.Checksums.SHA256)
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))

			if r.Method != http.MethodHead {
				_, _ = w.Write(body) //nolint:gosec // a compact index text file, served as octet-stream
			}

			return
		}
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if item.Repository != t.Repo && !h.RequireRepo(w, r, RepoCheck{
		item.Repository,
		item.Path(),
		auth.ActionRead,
	}) {
		return
	}

	h.serveItem(w, r, m, item)
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request, m Mount, item repository.Item) {
	ctx := r.Context()
	if item.IsFolder() {
		h.listing(w, r, item)
		return
	}

	if !h.allowDownload(w, r, item) {
		return
	}

	hdr := w.Header()
	hdr.Set("Content-Type", MimeType(item.Name))
	hdr.Set("Accept-Ranges", "bytes")
	hdr.Set("Last-Modified", item.Modified.UTC().Format(http.TimeFormat))
	hdr.Set("ETag", `"`+item.Checksums.SHA1+`"`)
	hdr.Set("X-Checksum-Sha1", item.Checksums.SHA1)
	hdr.Set("X-Checksum-Sha256", item.Checksums.SHA256)
	hdr.Set("X-Checksum-Md5", item.Checksums.MD5)
	m.fileHeaders(hdr, item)
	hdr.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": item.Name,
	}))

	rng, ok := parseRange(r.Header.Get("Range"), item.Size)
	if !ok {
		hdr.Set("Content-Range", "bytes */"+strconv.FormatInt(item.Size, 10))
		httperror.Write(w, r, http.StatusRequestedRangeNotSatisfiable, "requested range not satisfiable")

		return
	}

	status, length := http.StatusOK, item.Size
	if rng != nil {
		status, length = http.StatusPartialContent, rng.End-rng.Start+1
		hdr.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng.Start, rng.End, item.Size))
	}

	hdr.Set("Content-Length", strconv.FormatInt(length, 10))

	if r.Method == http.MethodHead || length == 0 {
		w.WriteHeader(status)
		return
	}

	body, err := h.repos.Open(ctx, item, rng)
	if err != nil {
		hdr.Del("Content-Length")
		h.writeError(w, r, err)

		return
	}

	defer func() {
		_ = body.Close()
	}()

	w.WriteHeader(status)

	if _, err := copyBody(w, body); err != nil {
		h.log.WarnContext(ctx, "download interrupted", "repo", item.Repository, "path", item.Path(), "error", err)

		panic(http.ErrAbortHandler)
	}
}

var copyBuffers = sync.Pool{
	New: func() any {
		return new([256 << 10]byte)
	},
}

func copyBody(w io.Writer, body io.Reader) (int64, error) {
	buf, _ := copyBuffers.Get().(*[256 << 10]byte)
	defer copyBuffers.Put(buf)

	// Hide ReaderFrom: http's own copies through a 32 KiB buffer.
	return io.CopyBuffer(struct {
		io.Writer
	}{
		w,
	}, body, buf[:])
}

func parseRange(header string, size int64) (*objectstore.Range, bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return nil, true
	}

	startS, endS, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return nil, true
	}

	var start, end int64

	switch startS {
	case "":
		n, err := strconv.ParseInt(endS, 10, 64)
		if err != nil || n <= 0 {
			return nil, false
		}

		start, end = max(size-n, 0), size-1
	default:
		var err error
		if start, err = strconv.ParseInt(startS, 10, 64); err != nil || start < 0 {
			return nil, false
		}

		end = size - 1

		if endS != "" {
			e, err := strconv.ParseInt(endS, 10, 64)
			if err != nil || e < start {
				return nil, false
			}

			end = min(e, size-1)
		}
	}

	if size == 0 || start >= size {
		return nil, false
	}

	return &objectstore.Range{
		Start: start,
		End:   end,
	}, true
}

// MimeType is the content type a file is served with, by its extension.
func MimeType(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}

	return "application/octet-stream"
}

func (h *Handler) listing(w http.ResponseWriter, r *http.Request, folder repository.Item) {
	children, err := h.repos.Children(r.Context(), folder)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if r.Method == http.MethodHead {
		return
	}

	var b strings.Builder

	title := html.EscapeString(folder.Repository + storagePath(folder))
	b.WriteString(
		"<!DOCTYPE html><html><head><title>Index of " + title + "</title></head><body><h1>Index of " + title + "</h1><pre>",
	)

	for _, c := range children {
		name := c.Name
		if c.IsFolder() {
			name += "/"
		}

		b.WriteString(`<a href="` + html.EscapeString(url.PathEscape(c.Name)))

		if c.IsFolder() {
			b.WriteString("/")
		}

		b.WriteString(`">` + html.EscapeString(name) + "</a>\n")
	}

	b.WriteString("</pre></body></html>\n")
	_, _ = io.WriteString(w, b.String())
}

func storagePath(item repository.Item) string {
	if item.Name == repository.RootPath {
		return "/"
	}

	return "/" + item.Path()
}
