package packages

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

func (h *Handler) rawAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	h.serveRaw(w, r, m, strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix))
}

func (h *Handler) serveRaw(w http.ResponseWriter, r *http.Request, m Mount, escapedRepoPath string) {
	p, err := url.PathUnescape(escapedRepoPath)
	if err != nil {
		h.writeError(w, r, repository.ErrInvalidPath)
		return
	}

	t := Target{
		Dir: strings.HasSuffix(p, "/"),
	}

	t.Repo, t.Path, _ = strings.Cut(p, "/")
	if t.Repo == "" {
		h.writeError(w, r, repository.ErrInvalidPath)
		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if h.RequireRepo(w, r, RepoCheck{
			Repo:   t.Repo,
			Path:   t.Path,
			Action: auth.ActionRead,
		}) {
			h.ServeFile(w, r, m, t)
		}
	case http.MethodPut:
		h.deployRaw(w, r, m, t)
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT")
		httperror.Write(w, r, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) deployRaw(w http.ResponseWriter, r *http.Request, m Mount, t Target) {
	ctx := r.Context()

	target, err := h.repos.DeployTarget(ctx, t.Repo)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	t.Repo = target

	checks := []RepoCheck{
		{
			Repo:   t.Repo,
			Path:   t.Path,
			Action: auth.ActionWrite,
		},
	}
	if _, err := h.repos.Stat(ctx, t.Repo, t.Path); err == nil && !t.Dir {
		checks = append(checks, RepoCheck{
			Repo:   t.Repo,
			Path:   t.Path,
			Action: auth.ActionDelete,
		})
	} else if err != nil && !errors.Is(err, repository.ErrNotFound) {
		h.writeError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, checks...) {
		return
	}

	user := auth.FromContext(ctx).Name()

	var item repository.Item

	switch {
	case t.Dir && r.ContentLength <= 0:
		item, err = h.repos.CreateFolder(ctx, t.Repo, t.Path, user)
	case IsChecksumName(t.Path):
		if ok, handled := h.VerifyChecksumUpload(w, r, t); handled {
			if ok {
				w.WriteHeader(http.StatusCreated)
			}

			return
		}

		fallthrough
	default:
		item, err = h.repos.Deploy(ctx, repository.DeployInput{
			Repository: t.Repo,
			Path:       t.Path,
			Body:       r.Body,
			Size:       r.ContentLength,
			User:       user,
			Authorize: func(itemPath string, overwrite bool) error {
				if !overwrite {
					return nil
				}

				canDelete, err := h.CanRepo(r, t.Repo, itemPath, auth.ActionDelete)
				if err != nil {
					return err
				}

				if !canDelete {
					return fmt.Errorf("%w: overwriting %s needs delete permission", repository.ErrForbidden, itemPath)
				}

				return nil
			},
			Expected: repository.Checksums{
				SHA1:   r.Header.Get("X-Checksum-Sha1"),
				MD5:    r.Header.Get("X-Checksum-Md5"),
				SHA256: r.Header.Get("X-Checksum-Sha256"),
			},
		})
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Location", m.base(r)+"/"+escapePath(item.Repository+"/"+item.Path()))
	writeJSON(w, http.StatusCreated, map[string]any{
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
