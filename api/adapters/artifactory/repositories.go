package artifactory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"relic/api/adapters/artifactory/repoconfig"
	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

const maxRepoConfigBytes = 1 << 20

type repositoryDetails struct {
	Key         string `json:"key"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url"`
	PackageType string `json:"packageType"`
}

func (h *Handler) listRepositories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var format repository.Format

	packageType := q.Get("packageType")
	if t := packageType; t != "" {
		f, ok := repoconfig.FormatOf(t)
		if !ok {
			wire.WriteJSON(w, http.StatusOK, []repositoryDetails{})
			return
		}

		format = f
	}

	repos, err := h.repos.ListRepositories(r.Context(), repository.Class(strings.ToLower(q.Get("type"))), format)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	project := q.Get("project")
	p := auth.FromContext(r.Context())

	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	out := make([]repositoryDetails, 0, len(repos))
	for _, rp := range repos {
		if project != "" && rp.Project != project {
			continue
		}

		if packageType != "" && !strings.EqualFold(repoconfig.WirePackageType(rp), packageType) {
			continue
		}

		if !perms.CanReadRepository(p.Subject(), auth.Repository{
			Name:  rp.Name,
			Class: string(rp.Class),
		}) {
			continue
		}

		out = append(out, repositoryDetails{
			Key:         rp.Name,
			Type:        strings.ToUpper(string(rp.Class)),
			URL:         baseURL(r) + "/" + rp.Name,
			PackageType: capitalize(repoconfig.WirePackageType(rp)),
		})
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

func (h *Handler) getRepository(w http.ResponseWriter, r *http.Request) {
	rp, err := h.repos.GetRepository(r.Context(), r.PathValue("key"))
	if errors.Is(err, repository.ErrRepositoryNotFound) {
		wire.WriteError(w, http.StatusBadRequest, "Bad Request")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	p := auth.FromContext(r.Context())
	if !p.Admin {
		h.writePartialRepository(w, r, p, rp)
		return
	}

	doc, err := repoconfig.ToArtifactory(rp)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(doc) //nolint:gosec // G705: admin-only JSON with nosniff
}

type partialRepository struct {
	Key         string `json:"key"`
	Rclass      string `json:"rclass"`
	PackageType string `json:"packageType"`
	Description string `json:"description"`
}

// writePartialRepository answers a non-administrator with the fields any reader of the repository may see,
// and as if the repository did not exist when they may not read it.
func (h *Handler) writePartialRepository(
	w http.ResponseWriter,
	r *http.Request,
	p auth.Principal,
	rp repository.Repository,
) {
	perms, err := h.security.Permissions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !perms.CanReadRepository(p.Subject(), auth.Repository{
		Name:  rp.Name,
		Class: string(rp.Class),
	}) {
		wire.WriteError(w, http.StatusBadRequest, "Bad Request")
		return
	}

	wire.WriteJSON(w, http.StatusOK, partialRepository{
		Key:         rp.Name,
		Rclass:      string(rp.Class),
		PackageType: repoconfig.WirePackageType(rp),
		Description: rp.Config.Description,
	})
}

func (h *Handler) createRepository(w http.ResponseWriter, r *http.Request) {
	rp, ok := h.readRepository(w, r, true)
	if !ok {
		return
	}

	if err := h.repos.CreateRepository(r.Context(), rp); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Repository "+rp.Name+" created.")
}

// updateRepository serves POST /api/repositories/{key}; exactly 200.
func (h *Handler) updateRepository(w http.ResponseWriter, r *http.Request) {
	rp, ok := h.readRepository(w, r, false)
	if !ok {
		return
	}

	if err := h.repos.UpdateRepository(r.Context(), rp); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Repository "+rp.Name+" updated.")
}

// deleteRepository serves DELETE /api/repositories/{key}; exactly 200.
func (h *Handler) deleteRepository(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if err := h.repos.DeleteRepository(r.Context(), key); err != nil {
		h.writeError(w, r, err)
		return
	}

	writeText(w, http.StatusOK, "Repository "+key+" deleted with its contents.")
}

func (h *Handler) readRepository(w http.ResponseWriter, r *http.Request, create bool) (repository.Repository, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRepoConfigBytes))
	if err != nil {
		wire.WriteBodyError(w, err, "repository configuration too large")
		return repository.Repository{}, false
	}

	rp, err := h.parseRepository(r, r.PathValue("key"), body, create)
	if err != nil {
		h.writeError(w, r, err)
		return repository.Repository{}, false
	}

	return rp, true
}

func (h *Handler) parseRepository(r *http.Request, key string, doc []byte, create bool) (repository.Repository, error) {
	if create {
		return repoconfig.FromArtifactory(key, doc)
	}

	existing, err := h.repos.GetRepository(r.Context(), key)
	if err != nil {
		return repository.Repository{}, err
	}

	return repoconfig.Merge(existing, doc)
}

func writeText(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg) //nolint:gosec // G705: text/plain with nosniff, never rendered as HTML
}

func (h *Handler) batchRepositories(w http.ResponseWriter, r *http.Request) {
	var docs []json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&docs); err != nil || len(docs) == 0 {
		wire.WriteError(w, http.StatusBadRequest, "expected a JSON array of repository configurations")
		return
	}

	repos := make([]repository.Repository, 0, len(docs))
	for i, doc := range docs {
		var head struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(doc, &head); err != nil || head.Key == "" {
			wire.WriteError(w, http.StatusBadRequest, fmt.Sprintf("entry %d has no key", i))
			return
		}

		rp, err := h.parseRepository(r, head.Key, doc, r.Method == http.MethodPut)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		repos = append(repos, rp)
	}

	create := r.Method == http.MethodPut

	seen := map[string]bool{}
	for _, rp := range repos {
		if err := h.checkBatchEntry(r.Context(), rp, create, seen); err != nil {
			h.writeError(w, r, fmt.Errorf("%s: %w", rp.Name, err))
			return
		}
	}

	for _, rp := range repos {
		var err error
		if create {
			err = h.repos.CreateRepository(r.Context(), rp)
		} else {
			err = h.repos.UpdateRepository(r.Context(), rp)
		}

		if err != nil {
			h.writeError(w, r, fmt.Errorf("%s: %w", rp.Name, err))
			return
		}
	}

	if create {
		w.WriteHeader(http.StatusCreated)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) checkBatchEntry(
	ctx context.Context,
	rp repository.Repository,
	create bool,
	seen map[string]bool,
) error {
	if seen[rp.Name] {
		return fmt.Errorf("%w: listed twice", repository.ErrRepositoryExists)
	}

	seen[rp.Name] = true

	if err := rp.Validate(); err != nil {
		return err
	}

	if !create {
		return nil
	}

	_, err := h.repos.GetRepository(ctx, rp.Name)
	if err == nil {
		return repository.ErrRepositoryExists
	}

	if !errors.Is(err, repository.ErrRepositoryNotFound) {
		return err
	}

	return nil
}
