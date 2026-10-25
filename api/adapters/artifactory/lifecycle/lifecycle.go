// Package lifecycle serves the Release Bundles v2 API under /lifecycle/api/v2.
package lifecycle

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/api/adapters/artifactory"
	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httpserver"

	rb "relic/internal/delivery/release"
)

const (
	prefix         = "/lifecycle/api/v2/"
	maxRequestBody = 16 << 20
	maxArchive     = 8 << 30
	timeFormat     = "2006-01-02T15:04:05.000Z07:00"
)

// Permissions returns the compiled permissions.
type Permissions interface {
	Permissions(ctx context.Context) (auth.Permissions, error)
}

// Handler serves the lifecycle API.
type Handler struct {
	bundles *rb.Service
	perms   Permissions
	log     *slog.Logger
}

// New returns a Handler.
func New(bundles *rb.Service, perms Permissions, log *slog.Logger) *Handler {
	return &Handler{
		bundles: bundles,
		perms:   perms,
		log:     log,
	}
}

// Register mounts the API on rt.
func (h *Handler) Register(rt *httpserver.Router) {
	p := prefix + "release_bundle"
	rt.HandleFunc("POST "+p, auth.Authenticated, signedWithReleaseKey(h.create))
	rt.HandleFunc("PATCH "+p, auth.Authenticated, h.update)
	rt.HandleFunc(
		"POST "+p+"/{name}/{version}/finalize",
		auth.Authenticated,
		h.release(auth.ActionWrite, signedWithReleaseKey(h.finalize)),
	)
	rt.HandleFunc("GET "+p+"/statuses/{name}/{version}", auth.Authenticated, h.release(auth.ActionRead, h.status))
	rt.HandleFunc("GET "+p+"/existence/{name}/{version}", auth.Authenticated, h.release(auth.ActionRead, h.existence))
	rt.HandleFunc("GET "+p+"/groups", auth.Authenticated, h.groups)
	rt.HandleFunc("GET "+p+"/records/{name}", auth.Authenticated, h.versions)
	rt.HandleFunc("GET "+p+"/records/{name}/{version}", auth.Authenticated, h.release(auth.ActionRead, h.version))
	rt.HandleFunc(
		"DELETE "+p+"/records/{name}/{version}",
		auth.Authenticated,
		h.release(auth.ActionDelete, h.deleteVersion),
	)
	rt.HandleFunc("PUT "+p+"/records/{name}/{version}/tag", auth.Authenticated, h.release(auth.ActionAnnotate, h.tag))

	p = prefix + "promotion/records/{name}/{version}"
	rt.HandleFunc("POST "+p, auth.Authenticated, h.release(auth.ActionPromote, signedWithReleaseKey(h.promote)))
	rt.HandleFunc("GET "+p, auth.Authenticated, h.release(auth.ActionRead, h.promotions))
	rt.HandleFunc("DELETE "+p+"/{created}", auth.Authenticated, h.release(auth.ActionPromote, h.deletePromotion))

	p = prefix + "distribution/"
	rt.HandleFunc(
		"POST "+p+"distribute/{name}/{version}",
		auth.Authenticated,
		h.release(auth.ActionPromote, h.distribute),
	)
	rt.HandleFunc(
		"POST "+p+"remote_delete/{name}/{version}",
		auth.Authenticated,
		h.release(auth.ActionDelete, h.remoteDelete),
	)
	rt.HandleFunc(
		"GET "+p+"trackers/{name}/{version}",
		auth.Authenticated,
		h.release(auth.ActionRead, h.remoteDeleteTrackers),
	)
	rt.HandleFunc(
		"GET "+p+"export/status/{name}/{version}",
		auth.Authenticated,
		h.release(auth.ActionRead, h.exportStatus),
	)
	rt.HandleFunc("POST "+p+"export/{name}/{version}", auth.Authenticated, h.release(auth.ActionWrite, h.export))

	rt.HandleFunc("POST /artifactory/api/release/import/", auth.Admin, h.importArchive)
}

const signingKeyHeader = "X-JFrog-Signing-Key"

func signedWithReleaseKey(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if key := r.Header.Get(signingKeyHeader); key != "" && key != rb.SigningKey {
			wire.WriteError(
				w,
				http.StatusBadRequest,
				"signing key "+strconv.Quote(key)+" is not available: release bundles are signed with "+rb.SigningKey,
			)

			return
		}

		next(w, r)
	}
}

func (h *Handler) can(w http.ResponseWriter, r *http.Request, name, version string, a auth.Action) (bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true, nil
	}

	perms, err := h.perms.Permissions(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return false, err
	}

	return perms.CanRelease(p.Subject(), layout.ReleaseBundles.Repository(project(r)), name, version, a), nil
}

func (h *Handler) release(a auth.Action, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.allowed(w, r, r.PathValue("name"), r.PathValue("version"), a) {
			next(w, r)
		}
	}
}

func (h *Handler) allowed(w http.ResponseWriter, r *http.Request, name, version string, a auth.Action) bool {
	ok, err := h.can(w, r, name, version, a)
	if err != nil {
		return false
	}

	if !ok {
		wire.WriteError(w, http.StatusForbidden, "the release permission does not grant "+string(a))
	}

	return ok
}

func (h *Handler) access(r *http.Request) (rb.Access, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return rb.Access{}, nil
	}

	perms, err := h.perms.Permissions(r.Context())
	if err != nil {
		return rb.Access{}, err
	}

	class := func(key string) auth.Repository {
		return auth.Repository{
			Name:  key,
			Class: "local",
		}
	}

	return rb.Access{
		CanRead: func(repository, path string) bool {
			return perms.CanInRepository(p.Subject(), class(repository), path, auth.ActionRead)
		},
		CanWrite: func(repository, path string) bool {
			return perms.CanInRepository(p.Subject(), class(repository), path, auth.ActionWrite)
		},
		CanDelete: func(repository, path string) bool {
			return perms.CanInRepository(p.Subject(), class(repository), path, auth.ActionDelete)
		},
		CanReadBuild: func(repository, name string) bool {
			return perms.CanBuild(p.Subject(), repository, name, auth.ActionRead)
		},
	}, nil
}

func project(r *http.Request) string { return r.URL.Query().Get("project") }

func user(r *http.Request) string { return auth.FromContext(r.Context()).Name() }

func async(r *http.Request) bool { return r.URL.Query().Get("async") == "true" }

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(v); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return false
	}

	return true
}

type bundleRef struct {
	RepoKey string `json:"repository_key"`
	Name    string `json:"release_bundle_name"`
	Version string `json:"release_bundle_version"`
	Created string `json:"created"`
}

func refOf(b rb.Release) bundleRef {
	return bundleRef{
		RepoKey: b.Repository,
		Name:    b.Name,
		Version: b.Version,
		Created: b.Created.Format(timeFormat),
	}
}

// create answers 201 when synchronous and 202 when asynchronous.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	req, ok := h.readSpec(w, r)
	if !ok {
		return
	}

	access, err := h.access(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	b, err := h.bundles.Create(r.Context(), project(r), req, user(r), access)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, cmp.Or(asyncStatus(r, http.StatusAccepted), http.StatusCreated), refOf(b))
}

func (h *Handler) readSpec(w http.ResponseWriter, r *http.Request) (rb.Spec, bool) {
	var body createBody
	if !decode(w, r, &body) || !h.allowed(w, r, body.Name, body.Version, auth.ActionWrite) {
		return rb.Spec{}, false
	}

	spec, err := body.spec()
	if err != nil {
		h.fail(w, r, err)
		return rb.Spec{}, false
	}

	return spec, true
}

func asyncStatus(r *http.Request, status int) int {
	if async(r) {
		return status
	}

	return 0
}

// update answers 200 in both modes.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	req, ok := h.readSpec(w, r)
	if !ok {
		return
	}

	access, err := h.access(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	b, err := h.bundles.Update(r.Context(), project(r), req, user(r), access)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, refOf(b))
}

// finalize answers 200 in both modes.
func (h *Handler) finalize(w http.ResponseWriter, r *http.Request) {
	b, err := h.bundles.Finalize(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"), user(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, refOf(b))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	b, err := h.bundles.Get(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"status": status(b.Status),
	})
}

func (h *Handler) existence(w http.ResponseWriter, r *http.Request) {
	_, err := h.bundles.Get(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if err != nil && !errors.Is(err, rb.ErrNotFound) {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]bool{
		"exists": err == nil,
	})
}

func page(r *http.Request, bundles []rb.Release, key func(rb.Release) string) ([]rb.Release, int, int, int) {
	q := r.URL.Query()
	if f := q.Get("filter_by"); f != "" {
		bundles = slices.DeleteFunc(bundles, func(b rb.Release) bool {
			return !strings.Contains(key(b), f)
		})
	}

	if q.Get("order_by") == "name" || q.Get("order_by") == "release_bundle_name" {
		slices.SortStableFunc(bundles, func(a, b rb.Release) int {
			return cmp.Compare(key(a), key(b))
		})
	} else {
		slices.SortStableFunc(bundles, func(a, b rb.Release) int {
			return a.Created.Compare(b.Created)
		})
	}

	if q.Get("order_asc") != "true" {
		slices.Reverse(bundles)
	}

	total := len(bundles)
	offset, _ := strconv.Atoi(q.Get("offset"))

	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit <= 0 {
		limit = 1000
	}

	offset = min(max(offset, 0), total)

	return bundles[offset:min(offset+limit, total)], total, limit, offset
}

func (h *Handler) groups(w http.ResponseWriter, r *http.Request) {
	all, err := h.bundles.Groups(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if project(r) != "" {
		stored := h.bundles.Layout().Repository(project(r))
		all = slices.DeleteFunc(all, func(b rb.Release) bool {
			return b.Repository != stored
		})
	}

	if all, err = h.readable(r, all); err != nil {
		h.fail(w, r, err)
		return
	}

	list, total, limit, offset := page(r, all, func(b rb.Release) string {
		return b.Name
	})

	type group struct {
		RepoKey  string `json:"repository_key"`
		Project  string `json:"project_key"`
		Name     string `json:"release_bundle_name"`
		Latest   string `json:"release_bundle_version_latest"`
		Count    int64  `json:"release_bundle_versions_count"`
		Created  string `json:"created"`
		Service  string `json:"service_id"`
		ProjName string `json:"project_name"`
	}

	out := make([]group, 0, len(list))
	for _, b := range list {
		out = append(out, group{
			RepoKey:  b.Repository,
			Project:  cmp.Or(b.Project, "default"),
			Name:     b.Name,
			Latest:   b.Version,
			Count:    b.Versions,
			Created:  b.Created.Format(time.RFC3339Nano),
			Service:  artifactory.ServiceID,
			ProjName: cmp.Or(b.Project, "default"),
		})
	}

	wire.WriteJSON(
		w,
		http.StatusOK,
		map[string]any{
			"release_bundles": out,
			"total":           total,
			"limit":           limit,
			"offset":          offset,
		},
	)
}

type versionJSON struct {
	Status        string `json:"status"`
	ReleaseStatus string `json:"release_status"`
	RepoKey       string `json:"repository_key"`
	Name          string `json:"release_bundle_name"`
	Version       string `json:"release_bundle_version"`
	Tag           string `json:"release_bundle_tag,omitempty"`
	Service       string `json:"service_id"`
	CreatedBy     string `json:"created_by"`
	Created       string `json:"created"`
}

func versionOf(b rb.Release) versionJSON {
	release := "RELEASED"
	if b.Draft {
		release = "DRAFT"
	}

	return versionJSON{
		Status:        status(b.Status),
		ReleaseStatus: release,
		RepoKey:       b.Repository,
		Name:          b.Name,
		Version:       b.Version,
		Tag:           b.Tag,
		Service:       artifactory.ServiceID,
		CreatedBy:     b.CreatedBy,
		Created:       b.Created.Format(time.RFC3339Nano),
	}
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	all, err := h.bundles.Versions(r.Context(), project(r), r.PathValue("name"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if all, err = h.readable(r, all); err != nil {
		h.fail(w, r, err)
		return
	}

	list, total, limit, offset := page(r, all, func(b rb.Release) string {
		return b.Version
	})

	out := make([]versionJSON, 0, len(list))
	for _, b := range list {
		out = append(out, versionOf(b))
	}

	wire.WriteJSON(
		w,
		http.StatusOK,
		map[string]any{
			"release_bundles": out,
			"total":           total,
			"limit":           limit,
			"offset":          offset,
		},
	)
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	b, err := h.bundles.Get(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, versionOf(b))
}

func (h *Handler) readable(r *http.Request, list []rb.Release) ([]rb.Release, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return list, nil
	}

	perms, err := h.perms.Permissions(r.Context())
	if err != nil {
		return nil, err
	}

	return slices.DeleteFunc(list, func(b rb.Release) bool {
		return !perms.CanRelease(p.Subject(), b.Repository, b.Name, b.Version, auth.ActionRead)
	}), nil
}

// deleteVersion serves the local delete (sync 204, async 200) and the
// all-sites delete (no async parameter, a JSON body; 200).
func (h *Handler) deleteVersion(w http.ResponseWriter, r *http.Request) {
	name, version := r.PathValue("name"), r.PathValue("version")
	if !r.URL.Query().Has("async") {
		if _, err := h.bundles.Get(r.Context(), project(r), name, version); err != nil {
			h.fail(w, r, err)
			return
		}

		w.WriteHeader(http.StatusOK)

		return
	}

	if err := h.bundles.Delete(r.Context(), project(r), name, version); err != nil {
		h.fail(w, r, err)
		return
	}

	w.WriteHeader(cmp.Or(asyncStatus(r, http.StatusOK), http.StatusNoContent))
}

// tag reads {"tag":"rc","exist":true}; an empty tag clears it.
func (h *Handler) tag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Tag string `json:"tag"`
	}
	if !decode(w, r, &req) {
		return
	}

	if err := h.bundles.SetTag(
		r.Context(),
		project(r),
		r.PathValue("name"),
		r.PathValue("version"),
		req.Tag,
	); err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"tag": req.Tag,
	})
}

type promotionJSON struct {
	Status        string   `json:"status"`
	RepoKey       string   `json:"repository_key"`
	Name          string   `json:"release_bundle_name"`
	Version       string   `json:"release_bundle_version"`
	Environment   string   `json:"environment"`
	Included      []string `json:"included_repository_keys"`
	Excluded      []string `json:"excluded_repository_keys"`
	Service       string   `json:"service_id"`
	CreatedBy     string   `json:"created_by"`
	Created       string   `json:"created"`
	CreatedMillis int64    `json:"created_millis"`
	Messages      []any    `json:"messages"`
}

func promotionOf(p rb.Promotion) promotionJSON {
	msgs := make([]any, 0, len(p.Messages))
	for _, m := range p.Messages {
		msgs = append(msgs, map[string]string{
			"text": m,
		})
	}

	return promotionJSON{
		Status:        status(p.Status),
		RepoKey:       p.Repository,
		Name:          p.Name,
		Version:       p.Version,
		Environment:   p.Environment,
		Included:      p.Include,
		Excluded:      p.Exclude,
		Service:       artifactory.ServiceID,
		CreatedBy:     p.CreatedBy,
		Created:       p.Created.Format(timeFormat),
		CreatedMillis: p.CreatedMillis,
		Messages:      msgs,
	}
}

// promote answers 201 when synchronous and 202 when asynchronous, always with a JSON body.
func (h *Handler) promote(w http.ResponseWriter, r *http.Request) {
	var body promoteBody
	if !decode(w, r, &body) {
		return
	}

	access, err := h.access(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	req := body.spec(r.URL.Query().Get("operation"))

	p, err := h.bundles.Promote(
		r.Context(),
		project(r),
		r.PathValue("name"),
		r.PathValue("version"),
		req,
		user(r),
		access,
	)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, cmp.Or(asyncStatus(r, http.StatusAccepted), http.StatusCreated), promotionOf(p))
}

func (h *Handler) promotions(w http.ResponseWriter, r *http.Request) {
	list, err := h.bundles.Promotions(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	out := make([]promotionJSON, 0, len(list))
	for _, p := range list {
		out = append(out, promotionOf(p))
	}

	wire.WriteJSON(
		w,
		http.StatusOK,
		map[string]any{
			"promotions": out,
			"total":      len(out),
			"limit":      len(out),
			"offset":     0,
		},
	)
}

func (h *Handler) deletePromotion(w http.ResponseWriter, r *http.Request) {
	created, err := strconv.ParseInt(r.PathValue("created"), 10, 64)
	if err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid promotion timestamp")
		return
	}

	if err := h.bundles.DeletePromotion(
		r.Context(),
		project(r),
		r.PathValue("name"),
		r.PathValue("version"),
		created,
	); err != nil {
		h.fail(w, r, err)
		return
	}

	w.WriteHeader(cmp.Or(asyncStatus(r, http.StatusOK), http.StatusNoContent))
}

func (h *Handler) distribute(w http.ResponseWriter, _ *http.Request) {
	wire.WriteError(
		w,
		http.StatusNotFound,
		"Relic does not distribute release bundles: there are no distribution targets",
	)
}

func (h *Handler) remoteDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := h.bundles.Get(r.Context(), project(r), r.PathValue("name"), r.PathValue("version")); err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusAccepted, map[string]any{})
}

func (h *Handler) remoteDeleteTrackers(w http.ResponseWriter, r *http.Request) {
	b, err := h.bundles.Get(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if err != nil {
		h.fail(w, r, err)
		return
	}

	now := time.Now().UTC().Format(timeFormat)
	wire.WriteJSON(w, http.StatusOK, []map[string]any{
		{
			"distribution_tracker_friendly_id": 1,
			"type":                             "delete",
			"release_bundle_name":              b.Name,
			"release_bundle_version":           b.Version,
			"storing_repository":               b.Repository,
			"status":                           status(rb.StatusCompleted),
			"distributed_by":                   user(r),
			"created":                          now,
			"start_time":                       now,
			"finish_time":                      now,
			"targets":                          []string{},
		},
	})
}

// exportStatus answers NOT_TRIGGERED until the archive exists.
func (h *Handler) exportStatus(w http.ResponseWriter, r *http.Request) {
	item, err := h.bundles.ExportArchive(r.Context(), project(r), r.PathValue("name"), r.PathValue("version"))
	if errors.Is(err, repository.ErrNotFound) {
		wire.WriteJSON(w, http.StatusOK, map[string]string{
			"status": "NOT_TRIGGERED",
		})

		return
	}

	if err != nil {
		h.fail(w, r, err)
		return
	}

	rel := "/" + item.Repository + "/" + item.Path()
	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"status":                status(rb.StatusCompleted),
		"relative_download_url": rel,
		"download_url":          artifactoryURL(r) + rel,
	})
}

func (h *Handler) export(w http.ResponseWriter, r *http.Request) {
	if _, err := h.bundles.Export(
		r.Context(),
		project(r),
		r.PathValue("name"),
		r.PathValue("version"),
		user(r),
	); err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusAccepted, map[string]string{
		"status": "IN_PROGRESS",
	})
}

func (h *Handler) importArchive(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.CreateTemp("", "relic-rbi-*.zip")
	if err != nil {
		h.fail(w, r, err)
		return
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	size, err := io.Copy(tmp, http.MaxBytesReader(w, r.Body, maxArchive))

	var fileErr *os.PathError
	if errors.As(err, &fileErr) {
		h.fail(w, r, err)
		return
	}

	if err != nil {
		wire.WriteError(w, http.StatusBadRequest, "archive unreadable or too large")
		return
	}

	_, err = h.bundles.Import(r.Context(), tmp, size, user(r))
	if errors.Is(err, rb.ErrExists) {
		wire.WriteError(w, http.StatusBadRequest, "Bundle already exists")
		return
	}

	if err != nil {
		h.fail(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusAccepted, map[string]string{
		"status": "IMPORTED",
	})
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, rb.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		wire.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, rb.ErrForbidden), errors.Is(err, repository.ErrForbidden):
		wire.WriteError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, rb.ErrExists), errors.Is(err, repository.ErrConflict), errors.Is(err, rb.ErrRefused):
		wire.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, rb.ErrInvalid), errors.Is(err, rb.ErrNotDraft), errors.Is(err, repository.ErrChecksumMismatch):
		wire.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.ErrorContext(
			r.Context(),
			"lifecycle request failed",
			"request_id",
			httpserver.RequestID(r.Context()),
			"error",
			err,
		)
		wire.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

func artifactoryURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}

	return scheme + "://" + r.Host + "/artifactory"
}
