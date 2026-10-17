// Package evidence serves the Evidence API (/evidence/api/v1), the part of
// the GraphQL API the client reads (/onemodel/api/v1/graphql), and the
// trusted keys API.
package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/access/security"
	"relic/internal/artifacts/repository"
	"relic/internal/delivery/release"
	"relic/internal/platform/httpserver"

	evd "relic/internal/delivery/evidence"
)

// Version is the reported Evidence service version.
const Version = "7.646.1"

const (
	maxBody    = 16 << 20
	timeFormat = "2006-01-02T15:04:05.000Z07:00"
)

// Handler serves the evidence APIs.
type Handler struct {
	evidence *evd.Service
	bundles  *release.Service
	repos    *repository.Service
	security *security.Service
	log      *slog.Logger
}

// New returns a Handler.
func New(
	evidence *evd.Service,
	bundles *release.Service,
	repos *repository.Service,
	sec *security.Service,
	log *slog.Logger,
) *Handler {
	return &Handler{
		evidence: evidence,
		bundles:  bundles,
		repos:    repos,
		security: sec,
		log:      log,
	}
}

// Register mounts the APIs on rt.
func (h *Handler) Register(rt *httpserver.Router) {
	rt.HandleFunc(
		"GET /evidence/api/v1/system/version",
		auth.Authenticated,
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, Version)
		},
	)
	rt.HandleFunc("POST /evidence/api/v1/subject/{path...}", auth.Authenticated, h.create)
	rt.HandleFunc("POST /onemodel/api/v1/graphql", auth.Authenticated, h.graphql)
	rt.HandleFunc("POST /artifactory/api/security/keys/trusted", auth.Admin, h.addKey)
	rt.HandleFunc("GET /artifactory/api/security/keys/trusted", auth.Admin, h.listKeys)
	rt.HandleFunc("DELETE /artifactory/api/security/keys/trusted/{kid}", auth.Admin, h.deleteKey)
}

func (h *Handler) can(r *http.Request, repoKey, path string, a auth.Action) (bool, error) {
	p := auth.FromContext(r.Context())
	if p.Admin {
		return true, nil
	}

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
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
	}, path, a), nil
}

type attachmentJSON struct {
	Name         string `json:"name"`
	SHA256       string `json:"sha256"`
	Type         string `json:"type"`
	DownloadPath string `json:"download_path"`
}

func downloadPath(e evd.Evidence) string { return evd.Repository + "/" + e.ItemPath }

func attachmentPath(a evd.Attachment) string {
	if a.Repository == "" {
		return ""
	}

	return a.Repository + "/" + a.Path
}

func envelopeAttachments(body []byte) ([]evd.AttachmentPlace, error) {
	var env struct {
		Attachments []struct {
			Repository string `json:"repository"`
			Path       string `json:"path"`
			SHA256     string `json:"sha256"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: not a DSSE envelope: %w", evd.ErrInvalid, err)
	}

	out := make([]evd.AttachmentPlace, 0, len(env.Attachments))
	for _, a := range env.Attachments {
		out = append(out, evd.AttachmentPlace(a))
	}

	return out, nil
}

// Creating evidence needs annotate permission on the subject.
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	repoKey, path, ok := strings.Cut(r.PathValue("path"), "/")
	if !ok || path == "" {
		wire.WriteError(w, http.StatusBadRequest, "subject must be <repo>/<path>")
		return
	}

	allowed, err := h.can(r, repoKey, path, auth.ActionAnnotate)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	if !allowed {
		wire.WriteError(w, http.StatusForbidden, "no permission to annotate "+repoKey+"/"+path)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		wire.WriteBodyError(w, err, "evidence too large")
		return
	}

	places, err := envelopeAttachments(body)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	user := auth.FromContext(r.Context()).Name()

	e, err := h.evidence.Create(r.Context(), evd.Input{
		Repository:  repoKey,
		Path:        path,
		Envelope:    body,
		Attachments: places,
		Provider:    r.URL.Query().Get("providerId"),
		User:        user,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}

	dir, name := repository.SplitPath(e.ItemPath)

	atts := make([]attachmentJSON, 0, len(e.Attachments))
	for _, a := range e.Attachments {
		atts = append(
			atts,
			attachmentJSON{
				Name:         a.Name,
				SHA256:       a.SHA256,
				Type:         a.Type,
				DownloadPath: attachmentPath(a),
			},
		)
	}

	wire.WriteJSON(w, http.StatusCreated, map[string]any{
		"repository":         evd.Repository,
		"path":               dir,
		"name":               name,
		"uri":                "/" + downloadPath(e),
		"sha256":             e.ItemSHA256,
		"predicate_category": e.PredicateCategory,
		"predicate_type":     e.PredicateType,
		"predicate_slug":     e.PredicateSlug,
		"created_at":         e.Created.Format(timeFormat),
		"created_by":         e.CreatedBy,
		"verified":           e.Verified,
		"provider_id":        e.ProviderID,
		"attachments":        atts,
	})
}

func (h *Handler) addKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Alias string `json:"alias"`
		Key   string `json:"key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody)).Decode(&req); err != nil {
		wire.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})

		return
	}

	k, err := h.evidence.AddTrustedKey(r.Context(), req.Alias, req.Key, auth.FromContext(r.Context()).Name())
	switch {
	case errors.Is(err, evd.ErrKeyExists):
		wire.WriteJSON(
			w,
			http.StatusConflict,
			map[string]string{
				"alias": req.Alias,
				"error": "key alias " + req.Alias + " already exists",
			},
		)
	case errors.Is(err, evd.ErrInvalid):
		wire.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"alias": req.Alias,
			"error": err.Error(),
		})
	case err != nil:
		h.fail(w, r, err)
	default:
		wire.WriteJSON(w, http.StatusCreated, map[string]string{
			"alias":   k.Alias,
			"kid":     k.KID,
			"message": "key added",
		})
	}
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.evidence.TrustedKeys(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}

	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{
			"kid":         k.KID,
			"type":        k.Type,
			"alias":       k.Alias,
			"fingerprint": k.Fingerprint,
			"issuedBy":    k.IssuedBy,
			"issued":      k.Issued.UnixMilli(),
			"expiry":      0,
		})
	}

	wire.WriteJSON(w, http.StatusOK, map[string]any{
		"keys": out,
	})
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if err := h.evidence.DeleteTrustedKey(r.Context(), r.PathValue("kid")); err != nil {
		h.fail(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, repository.ErrNotFound),
		errors.Is(err, repository.ErrRepositoryNotFound),
		errors.Is(err, release.ErrNotFound):
		wire.WriteError(w, http.StatusNotFound, "404 Not Found: "+err.Error())
	case errors.Is(err, evd.ErrKeyNotFound):
		wire.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, evd.ErrInvalid):
		wire.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		h.log.ErrorContext(
			r.Context(),
			"evidence request failed",
			"request_id",
			httpserver.RequestID(r.Context()),
			"error",
			err,
		)
		wire.WriteError(w, http.StatusInternalServerError, "internal server error")
	}
}

func formatTime(t time.Time) string { return t.UTC().Format(timeFormat) }
