package packages

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

// CanRepo evaluates a repository permission for the request's principal.
func (h *Handler) CanRepo(r *http.Request, repoKey, itemPath string, a auth.Action) (bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true, nil
	}

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return true, nil
	}

	if err != nil {
		return false, err
	}

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		return false, err
	}

	return perms.CanInRepository(p.Subject(), auth.Repository{
		Name:  rp.Name,
		Class: string(rp.Class),
	}, itemPath, a), nil
}

// RepositoryPermits is CanRepo for paths in the given repositories,
// answered without further lookups; it is nil for an administrator.
func (h *Handler) RepositoryPermits(
	r *http.Request,
	repoKeys ...string,
) (func(repoKey, path string, a auth.Action) bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return nil, nil
	}

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		return nil, err
	}

	targets := make(map[string]auth.Repository, len(repoKeys))
	for _, key := range repoKeys {
		rp, err := h.repos.GetRepository(r.Context(), key)
		if err != nil {
			return nil, err
		}

		targets[key] = auth.Repository{
			Name:  rp.Name,
			Class: string(rp.Class),
		}
	}

	return func(repoKey, path string, a auth.Action) bool {
		target, ok := targets[repoKey]

		return ok && perms.CanInRepository(p.Subject(), target, path, a)
	}, nil
}

// CanRepoAnywhere is CanRepo for some path in the repository.
func (h *Handler) CanRepoAnywhere(r *http.Request, repoKey string, a auth.Action) (bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true, nil
	}

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		return true, nil
	}

	if err != nil {
		return false, err
	}

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		return false, err
	}

	return perms.CanAnywhereInRepository(p.Subject(), auth.Repository{
		Name:  rp.Name,
		Class: string(rp.Class),
	}, a), nil
}

func (h *Handler) visibleListings(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			ctx, err := h.visibility(r.Context())
			if err != nil {
				h.writeError(w, r, err)
				return
			}

			r = r.WithContext(ctx)
		}

		next(w, r)
	}
}

func (h *Handler) visibility(ctx context.Context) (context.Context, error) {
	p := auth.FromContext(ctx)
	if p.Admin {
		return ctx, nil
	}

	perms, err := h.security.Permissions(ctx)
	if err != nil {
		return nil, err
	}

	return repository.WithVisibility(
		ctx,
		&visibility{
			ctx:     ctx,
			h:       h,
			perms:   perms,
			subject: p.Subject(),
			repos:   map[string]visibleRepo{},
		},
	), nil
}

// Services may list from several goroutines.
type visibility struct {
	ctx     context.Context //nolint:containedctx // lives for one request
	h       *Handler
	perms   auth.Permissions
	subject auth.Subject

	mu    sync.Mutex
	repos map[string]visibleRepo
}

type visibleRepo struct {
	repo auth.Repository
	all  bool
}

func (v *visibility) repo(key string) visibleRepo {
	v.mu.Lock()
	vr, ok := v.repos[key]
	v.mu.Unlock()

	if ok {
		return vr
	}

	vr.repo = auth.Repository{
		Name: key,
	}

	rp, err := v.h.repos.GetRepository(v.ctx, key)
	if err != nil && !errors.Is(err, repository.ErrRepositoryNotFound) {
		v.h.log.ErrorContext(v.ctx, "listing visibility: repository lookup failed", "repository", key, "error", err)
	} else {
		vr.repo.Class = string(rp.Class)
		vr.all = v.perms.CanReadAll(v.subject, vr.repo)
	}

	v.mu.Lock()
	v.repos[key] = vr
	v.mu.Unlock()

	return vr
}

func (v *visibility) Restricted(repoKey string) bool { return !v.repo(repoKey).all }

func (v *visibility) Visible(repoKey, path string) bool {
	return v.perms.CanInRepository(v.subject, v.repo(repoKey).repo, path, auth.ActionRead)
}

// RequireRepo writes 403 and returns false unless every check passes.
func (h *Handler) RequireRepo(w http.ResponseWriter, r *http.Request, checks ...RepoCheck) bool {
	for _, c := range checks {
		ok, err := h.CanRepo(r, c.Repo, c.Path, c.Action)
		if err != nil {
			h.writeError(w, r, err)
			return false
		}

		if !ok {
			h.refuse(w, r, c)
			return false
		}
	}

	return true
}

func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, c RepoCheck) {
	usable, err := h.usable(r, c.Repo)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if usable {
		Forbidden(w, r, string(c.Action), c.Repo+"/"+c.Path)
		return
	}

	httperror.Write(w, r, http.StatusNotFound, "repository not found")
}

func (h *Handler) usable(r *http.Request, repoKey string) (bool, error) {
	for _, a := range auth.Actions {
		ok, err := h.CanRepoAnywhere(r, repoKey, a)
		if err != nil || ok {
			return ok, err
		}
	}

	return false, nil
}

// RepoCheck is one permission RequireRepo checks: action on path in repo.
type RepoCheck struct {
	Repo, Path string
	Action     auth.Action
}

// Forbidden answers 403 for a missing permission.
func Forbidden(w http.ResponseWriter, r *http.Request, action, target string) {
	httperror.Write(w, r, http.StatusForbidden, "Forbidden: missing '"+action+"' permission on "+target)
}

// CanReadContent reports whether the caller may read and download some
// stored file with the given content, or repository.ErrForbidden when every
// readable copy is download-blocked.
func (h *Handler) CanReadContent(r *http.Request, sums repository.Checksums) (bool, error) {
	ctx := r.Context()

	items, err := h.repos.FilesWithContent(ctx, sums)
	if err != nil {
		return false, err
	}

	var blocked []repository.Item

	for _, it := range items {
		ok := auth.FromContext(ctx).Admin
		if !ok {
			ok, err = h.CanRepo(r, it.Repository, it.Path(), auth.ActionRead)
			if err != nil {
				return false, err
			}
		}

		if !ok {
			continue
		}

		if h.blocker == nil {
			return true, nil
		}

		d, err := h.blocker.Explain(ctx, it.Repository, it.Path(), it.Checksums.SHA256)
		if err != nil {
			return false, err
		}

		if !d.Blocked {
			return true, nil
		}

		blocked = append(blocked, it)
	}

	if len(blocked) > 0 {
		return false, h.RefuseCopy(ctx, blocked[0])
	}

	return false, nil
}

// CanServe reports whether the caller may read the item a lookup actually
// found.
func (h *Handler) CanServe(r *http.Request, it repository.Item) (bool, error) {
	return h.CanRepo(r, it.Repository, it.Path(), auth.ActionRead)
}
