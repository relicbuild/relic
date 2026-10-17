package artifactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/wire"
	"relic/api/packages"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

// A literal ';' separates matrix parameters; a ';' inside a name arrives as %3B.
// '+' in the path is a literal plus.
func parseTarget(escapedPath string) (packages.Target, error) {
	rest, ok := strings.CutPrefix(escapedPath, "/artifactory/")
	if !ok {
		return packages.Target{}, repository.ErrInvalidPath
	}

	segments := strings.Split(rest, ";")

	p, err := url.PathUnescape(segments[0])
	if err != nil {
		return packages.Target{}, repository.ErrInvalidPath
	}

	t := packages.Target{
		Dir: strings.HasSuffix(p, "/"),
	}

	t.Repo, t.Path, _ = strings.Cut(p, "/")
	if t.Repo == "" {
		return packages.Target{}, repository.ErrInvalidPath
	}

	if len(segments) > 1 {
		if t.Props, err = parseProps(segments[1:]); err != nil {
			return packages.Target{}, fmt.Errorf("%w: %w", repository.ErrInvalidPath, err)
		}
	}

	return t, nil
}

func (h *Handler) files(w http.ResponseWriter, r *http.Request) {
	t, err := parseTarget(r.URL.EscapedPath())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if r.Method != http.MethodPut {
		remote, ok, err := layout.RemoteOfCache(r.Context(), h.repos, t.Repo)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		if ok {
			t.Repo, t.Cached = remote, true
		}
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if h.packages.RequireRepo(w, r, packages.RepoCheck{
			Repo:   t.Repo,
			Path:   t.Path,
			Action: auth.ActionRead,
		}) {
			h.packages.ServeFile(w, r, h.mount("/artifactory/"), t)
		}
	case http.MethodPut:
		target, err := h.repos.DeployTarget(r.Context(), t.Repo)
		if err != nil && !errors.Is(err, repository.ErrRepositoryNotFound) {
			h.writeError(w, r, err)
			return
		}

		if err == nil {
			t.Repo = target
		}

		checks := []packages.RepoCheck{
			{
				Repo:   t.Repo,
				Path:   t.Path,
				Action: auth.ActionWrite,
			},
		}
		if _, err := h.repos.Stat(r.Context(), t.Repo, t.Path); err == nil && !t.Dir {
			checks = append(checks, packages.RepoCheck{
				Repo:   t.Repo,
				Path:   t.Path,
				Action: auth.ActionDelete,
			})
		}

		if h.packages.RequireRepo(w, r, checks...) {
			h.deploy(w, r, t)
		}
	case http.MethodDelete:
		if h.packages.RequireRepo(w, r, packages.RepoCheck{
			Repo:   t.Repo,
			Path:   t.Path,
			Action: auth.ActionDelete,
		}) {
			h.remove(w, r, t)
		}
	default:
		w.Header().Set("Allow", "GET, HEAD, PUT, DELETE")
		wire.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) deploy(w http.ResponseWriter, r *http.Request, t packages.Target) {
	ctx := r.Context()

	user := auth.FromContext(ctx).Name()
	if t.Dir && r.ContentLength <= 0 {
		item, err := h.repos.CreateFolder(ctx, t.Repo, t.Path, user)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		wire.WriteJSON(w, http.StatusCreated, newFolderInfo(r, item, nil))

		return
	}

	in := repository.DeployInput{
		Repository: t.Repo,
		Path:       t.Path,
		Body:       r.Body,
		Size:       r.ContentLength,
		Props:      t.Props,
		User:       user,
		Expected: repository.Checksums{
			SHA1:   r.Header.Get("X-Checksum-Sha1"),
			MD5:    r.Header.Get("X-Checksum-Md5"),
			SHA256: firstNonEmpty(r.Header.Get("X-Checksum-Sha256"), r.Header.Get("X-Checksum")),
		},
	}

	var (
		item repository.Item
		err  error
	)

	switch {
	case strings.EqualFold(r.Header.Get("X-Checksum-Deploy"), "true"):
		readable, cerr := h.packages.CanReadContent(r, in.Expected)
		if cerr != nil {
			h.writeError(w, r, cerr)
			return
		}
		// Content the caller cannot read is treated as unknown.
		err = repository.ErrChecksumUnknown
		if readable {
			item, err = h.repos.DeployByChecksum(ctx, in)
		}

		if errors.Is(err, repository.ErrChecksumUnknown) {
			wire.WriteError(w, http.StatusNotFound, "No stored content has this checksum.")
			return
		}
	case strings.EqualFold(r.Header.Get("X-Explode-Archive"), "true"):
		// Every entry is its own deploy: check each path, and delete
		// permission where it overwrites.
		in.Authorize = func(p string, overwrite bool) error {
			for _, a := range []auth.Action{
				auth.ActionWrite,
				auth.ActionDelete,
			} {
				if a == auth.ActionDelete && !overwrite {
					continue
				}

				ok, err := h.packages.CanRepo(r, in.Repository, p, a)
				if err != nil {
					return err
				}

				if !ok {
					return fmt.Errorf("%w: %s on %s/%s", repository.ErrForbidden, a, in.Repository, p)
				}
			}

			return nil
		}
		if _, err := h.repos.Explode(ctx, in); err != nil {
			h.writeError(w, r, err)
			return
		}

		w.WriteHeader(http.StatusOK)

		return
	case packages.IsChecksumName(t.Path):
		if ok, handled := h.packages.VerifyChecksumUpload(w, r, t); handled {
			if ok {
				w.WriteHeader(http.StatusCreated)
			}

			return
		}

		item, err = h.repos.Deploy(ctx, in)
	default:
		item, err = h.repos.Deploy(ctx, in)
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	info := newFileInfo(r, item)
	w.Header().Set("Location", info.DownloadURI)
	w.Header().Set("X-Checksum-Sha256", item.Checksums.SHA256)
	w.Header().Set("Content-Type", "application/vnd.org.jfrog.artifactory.storage.ItemCreated+json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(info)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}

	return ""
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request, t packages.Target) {
	permits, err := h.packages.RepositoryPermits(r, t.Repo)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	var refuseFile func(context.Context, repository.Item) error
	if permits != nil {
		refuseFile = func(_ context.Context, file repository.Item) error {
			switch {
			case !permits(file.Repository, file.Path(), auth.ActionRead):
				return fmt.Errorf(
					"%w: missing 'delete' permission on everything under %s/%s",
					repository.ErrForbidden,
					t.Repo,
					t.Path,
				)
			case !permits(file.Repository, file.Path(), auth.ActionDelete):
				return fmt.Errorf(
					"%w: missing 'delete' permission on %s/%s",
					repository.ErrForbidden,
					file.Repository,
					file.Path(),
				)
			}

			return nil
		}
	}

	user := auth.FromContext(r.Context()).Name()
	if err := h.repos.DeleteToTrashChecked(r.Context(), t.Repo, t.Path, user, refuseFile); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type copyMessages struct {
	Messages []copyMessage `json:"messages"`
}

type copyMessage struct {
	Level   string `json:"level"`
	Message string `json:"message"`
}

func (h *Handler) copyItem(w http.ResponseWriter, r *http.Request) { h.copyOrMove(w, r, false) }
func (h *Handler) moveItem(w http.ResponseWriter, r *http.Request) { h.copyOrMove(w, r, true) }

// A "to" ending with '/' names the destination folder.
func (h *Handler) copyOrMove(w http.ResponseWriter, r *http.Request, move bool) {
	srcRepo, srcPath, err := h.repoPath(r, r.PathValue("path"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	q := rawQuery(r.URL.RawQuery)

	to := strings.TrimPrefix(queryValue(q, "to"), "/")
	if to == "" {
		wire.WriteError(w, http.StatusBadRequest, "missing 'to' parameter")
		return
	}

	src, err := h.repos.Stat(r.Context(), srcRepo, srcPath)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	destRepo, destPath, _ := strings.Cut(to, "/")
	if destPath == "" || strings.HasSuffix(destPath, "/") {
		destPath = path.Join(destPath, src.Name)
	}

	checks := []packages.RepoCheck{
		{
			Repo:   srcRepo,
			Path:   srcPath,
			Action: auth.ActionRead,
		},
		{
			Repo:   destRepo,
			Path:   destPath,
			Action: auth.ActionWrite,
		},
	}
	if move {
		checks = append(checks, packages.RepoCheck{
			Repo:   srcRepo,
			Path:   srcPath,
			Action: auth.ActionDelete,
		})
	}

	if !h.packages.RequireRepo(w, r, checks...) {
		return
	}

	dry := queryValue(q, "dry") == "1" || queryValue(q, "dry") == "true"

	permits, err := h.packages.RepositoryPermits(r, srcRepo, destRepo)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	err = h.repos.Copy(r.Context(), repository.CopyInput{
		Source:                src,
		DestinationRepository: destRepo,
		DestinationPath:       destPath,
		Move:                  move,
		DryRun:                dry,
		User:                  auth.FromContext(r.Context()).Name(),
		RefuseFile:            h.packages.RefuseCopy,
		Permits:               permits,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	verb := "Copied"
	if move {
		verb = "Moved"
	}

	wire.WriteJSON(w, http.StatusOK, copyMessages{
		Messages: []copyMessage{
			{
				Level: "INFO",
				Message: fmt.Sprintf(
					"%s %s/%s to %s/%s.",
					verb,
					src.Repository,
					src.Path(),
					destRepo,
					destPath,
				),
			},
		},
	})
}

func (h *Handler) restoreTrash(w http.ResponseWriter, r *http.Request) {
	from := strings.Trim(r.PathValue("path"), "/")

	toRepo, toPath, _ := strings.Cut(strings.Trim(r.URL.Query().Get("to"), "/"), "/")
	if toRepo != "" && toPath == "" {
		_, toPath, _ = strings.Cut(from, "/")
	}

	err := h.repos.RestoreFromTrash(r.Context(), from, toRepo, toPath, auth.FromContext(r.Context()).Name())
	if errors.Is(err, repository.ErrNoRestoreTarget) {
		wire.WriteError(w, http.StatusBadRequest, "no restore destination for "+from)
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Trash items restored.")
}

func (h *Handler) emptyTrash(w http.ResponseWriter, r *http.Request) {
	if err := h.repos.EmptyTrash(r.Context()); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Trash emptied.")
}

func (h *Handler) cleanTrash(w http.ResponseWriter, r *http.Request) {
	if err := h.repos.Delete(
		r.Context(),
		h.repos.TrashRepository(),
		strings.Trim(r.PathValue("path"), "/"),
	); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
