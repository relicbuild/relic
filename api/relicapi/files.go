package relicapi

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect/v2"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/objectstore"
)

// FilesPath is where file contents are read and written:
// GET/HEAD/PUT FilesPath + "<repository>/<path>".
const FilesPath = "/api/v1/files/"

func (d Dependencies) files(w http.ResponseWriter, r *http.Request) {
	key, p, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, FilesPath), "/")

	p = cleanPath(p)
	if key == "" || p == "" {
		writeHTTPError(
			w,
			connect.NewError(connect.CodeInvalidArgument, "expected "+FilesPath+"<repository>/<path>"),
		)

		return
	}

	if auth.FromContext(r.Context()).Anonymous() {
		writeHTTPError(w, connect.NewError(connect.CodeUnauthenticated, "sign in first"))
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		d.download(w, r, key, p)
	case http.MethodPut:
		d.upload(w, r, key, p)
	default:
		writeMethodNotAllowed(w, "GET, HEAD, PUT")
	}
}

func (d Dependencies) download(w http.ResponseWriter, r *http.Request, key, p string) {
	ctx := r.Context()
	if err := can(ctx, d, key, p, auth.ActionRead); err != nil {
		writeHTTPError(w, codeOf(err))
		return
	}

	item, err := d.Repositories.Resolve(ctx, key, p)
	if err == nil && item.Repository != key {
		err = can(ctx, d, item.Repository, item.Path(), auth.ActionRead)
	}

	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	if item.IsFolder() {
		writeHTTPError(
			w,
			connect.NewError(
				connect.CodeInvalidArgument,
				key+"/"+p+" is a folder: list it with ArtifactService.ListChildren",
			),
		)

		return
	}

	if d.DownloadBlocker != nil {
		decision, err := d.DownloadBlocker.Check(ctx, item.Repository, item.Path(), item.Checksums.SHA256)
		if err != nil {
			writeHTTPError(w, d.logged(r, err))
			return
		}

		if decision.Blocked {
			writeHTTPError(
				w,
				connect.NewError(
					connect.CodePermissionDenied,
					"download blocked: "+decision.Reason,
				),
			)

			return
		}
	}

	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		d.Repositories.RecordDownload(ctx, item)

		if d.DownloadStats != nil {
			d.DownloadStats.Download(item.ID, item.Repository, auth.FromContext(ctx).Name())
		}
	}

	h := w.Header()
	h.Set("X-Checksum-Sha1", item.Checksums.SHA1)
	h.Set("X-Checksum-Sha256", item.Checksums.SHA256)
	h.Set("X-Checksum-Md5", item.Checksums.MD5)
	h.Set("ETag", `"`+item.Checksums.SHA256+`"`)
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": item.Name,
	}))

	var content io.ReadSeeker = io.NewSectionReader(d.Repositories.ReaderAt(ctx, item), 0, item.Size)

	if r.Method == http.MethodGet {
		body, err := d.Repositories.ReadSeeker(ctx, item)
		if err != nil {
			writeHTTPError(w, d.logged(r, err))
			return
		}

		defer func() {
			_ = body.Close()
		}()

		content = body
	}

	http.ServeContent(w, r, item.Name, item.Modified, content)
}

func (d Dependencies) upload(w http.ResponseWriter, r *http.Request, key, p string) {
	ctx := r.Context()

	if _, err := readableRepository(ctx, d, key); err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	target, err := d.Repositories.DeployTarget(ctx, key)
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	action := auth.ActionWrite
	if _, err := d.Repositories.Stat(ctx, target, p); err == nil {
		action = auth.ActionDelete

		if err := can(ctx, d, target, p, auth.ActionWrite); err != nil {
			writeHTTPError(w, codeOf(err))
			return
		}
	}

	if err := can(ctx, d, target, p, action); err != nil {
		writeHTTPError(w, codeOf(err))
		return
	}

	props, err := uploadProperties(r.URL.Query())
	if err != nil {
		writeHTTPError(w, codeOf(err))
		return
	}

	if len(props) > 0 {
		if err := can(ctx, d, target, p, auth.ActionAnnotate); err != nil {
			writeHTTPError(w, codeOf(err))
			return
		}
	}

	check, err := repositoryAccess(ctx, d, target)
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	item, err := d.Repositories.Deploy(ctx, repository.DeployInput{
		Repository: target,
		Path:       p,
		Body:       r.Body,
		Size:       r.ContentLength,
		Props:      props,
		User:       auth.FromContext(ctx).Name(),
		Authorize: func(itemPath string, overwrite bool) error {
			if !overwrite {
				return nil
			}

			return check(itemPath, auth.ActionDelete)
		},
		Expected: repository.Checksums{
			SHA256: r.Header.Get("X-Checksum-Sha256"),
			SHA1:   r.Header.Get("X-Checksum-Sha1"),
			MD5:    r.Header.Get("X-Checksum-Md5"),
		},
	})
	if err != nil {
		writeHTTPError(w, d.logged(r, err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errchkjson // plain values
		"repository": item.Repository,
		"path":       item.Path(),
		"size":       item.Size,
		"checksums": map[string]string{
			"sha1":   item.Checksums.SHA1,
			"sha256": item.Checksums.SHA256,
			"md5":    item.Checksums.MD5,
		},
	})
}

// uploadProperties reads the properties an upload sets: each query
// parameter is one, a repeated parameter gives it several values.
func uploadProperties(query url.Values) (repository.Properties, error) {
	if len(query) == 0 {
		return nil, nil
	}

	props := repository.Properties{}

	for k, values := range query {
		if err := validProperty(k, values); err != nil {
			return nil, err
		}

		props[k] = values
	}

	return props, nil
}

func (d Dependencies) logged(r *http.Request, err error) *connect.Error {
	ce := codeOf(err)
	if ce.Code() == connect.CodeInternal {
		d.Log.ErrorContext(r.Context(), "relic api files", "path", r.URL.Path, "error", err)
	}

	return ce
}

func writeHTTPError(w http.ResponseWriter, ce *connect.Error) {
	status := restHTTPStatus(ce.Code())

	switch {
	case errors.Is(ce, repository.ErrTooLarge), errors.Is(ce, repository.ErrQuotaExceeded):
		status = http.StatusRequestEntityTooLarge
	case errors.Is(ce, objectstore.ErrStorageFull):
		status = http.StatusInsufficientStorage
	}

	writeRESTError(w, status, ce)
}

func writeMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeRESTError(w, http.StatusMethodNotAllowed, connect.NewError(connect.CodeUnimplemented, "method not allowed"))
}
