// Package access serves the adapter's token and login API under /access.
package access

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/access/ldap"
	"relic/internal/access/security"
	"relic/internal/access/workload"
	"relic/internal/platform/httpserver"
)

// Handler serves the Access API.
type Handler struct {
	tokens    *auth.Tokens
	logins    *auth.WebLogins
	ldap      *ldap.Service
	security  *security.Service
	workloads *workload.Service
	log       *slog.Logger
}

// New returns a Handler.
func New(tokens *auth.Tokens, logins *auth.WebLogins, log *slog.Logger) *Handler {
	return &Handler{
		tokens: tokens,
		logins: logins,
		log:    log,
	}
}

// Register mounts the API on rt.
func (h *Handler) Register(rt *httpserver.Router) {
	rt.HandleFunc("GET /access/api/v1/system/ping", auth.Public, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("OK"))
	})
	// Public: the handler authorizes creation itself.
	rt.HandleAudited("POST /access/api/v1/tokens", auth.Public, http.HandlerFunc(h.createToken))
	rt.HandleFunc("POST /access/api/v2/authentication/jfrog_client_login/request", auth.Public, h.loginRequest)
	// Public: a CI job exchanges its own OIDC token; the workload identity decides.
	rt.HandleFunc("POST /access/api/v1/oidc/token", auth.Public, h.exchangeOIDCToken)
	rt.HandleFunc("GET /access/api/v2/authentication/jfrog_client_login/token/{session}", auth.Public, h.loginToken)

	if h.ldap != nil {
		h.registerLDAP(rt)
	}

	if h.security != nil {
		h.registerV2(rt)
	}
}

type createTokenRequest struct {
	Scope                 string `json:"scope"`
	AccessToken           string `json:"access_token"`
	ExpiresIn             *int64 `json:"expires_in"`
	Refreshable           bool   `json:"refreshable"`
	RefreshToken          string `json:"refresh_token"`
	GrantType             string `json:"grant_type"`
	Audience              string `json:"audience"`
	IncludeReferenceToken bool   `json:"include_reference_token"`
	Username              string `json:"username"`
	ProjectKey            string `json:"project_key"`
	Description           string `json:"description"`
}

const defaultExpiry = 365 * 24 * time.Hour

func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid token request")
		return
	}

	ctx := r.Context()
	if req.GrantType == "refresh_token" {
		tok, err := h.tokens.Refresh(ctx, req.RefreshToken)
		if errors.Is(err, auth.ErrInvalidToken) {
			wire.WriteError(w, http.StatusUnauthorized, "invalid refresh token")
			return
		}

		if err != nil {
			h.fail(w, r, err)
			return
		}

		wire.WriteJSON(w, http.StatusOK, Response(tok, RenderScope))

		return
	}

	caller := auth.FromContext(ctx)
	if caller.Anonymous() {
		auth.Unauthorized(w, r)
		return
	}

	username := req.Username
	if username == "" || strings.EqualFold(username, caller.Username) {
		username = caller.Username
	}

	scope, known := ParseScope(req.Scope)
	if !known {
		wire.WriteError(
			w,
			http.StatusBadRequest,
			"unsupported scope: use applied-permissions/user, applied-permissions/admin or applied-permissions/groups",
		)

		return
	}

	if !caller.Admin && (username != caller.Username || !caller.MayGrant(scope)) {
		wire.WriteError(
			w,
			http.StatusForbidden,
			"only administrators can create tokens for other users or with admin or foreign group scopes",
		)

		return
	}

	expiry := defaultExpiry

	if req.ExpiresIn != nil {
		if *req.ExpiresIn < 0 {
			wire.WriteError(w, http.StatusBadRequest, "expires_in must not be negative")
			return
		}

		if *req.ExpiresIn > math.MaxInt64/int64(time.Second) {
			wire.WriteError(w, http.StatusBadRequest, "expires_in is too large")
			return
		}

		expiry = time.Duration(*req.ExpiresIn) * time.Second
	}

	tok, err := h.tokens.Issue(ctx, caller.LimitIssue(auth.IssueParams{
		Username:    username,
		Scope:       scope,
		ExpiresIn:   expiry,
		Refreshable: req.Refreshable,
		Description: req.Description,
		Project:     req.ProjectKey,
	}, time.Now()))
	if errors.Is(err, auth.ErrUnknownUser) {
		wire.WriteError(w, http.StatusNotFound, err.Error())
		return
	}

	if err != nil {
		h.fail(w, r, err)
		return
	}

	resp := Response(tok, RenderScope)
	if req.IncludeReferenceToken {
		resp.ReferenceToken = tok.AccessToken
	}

	wire.WriteJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Session string `json:"session"`
}

func (h *Handler) loginRequest(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		wire.WriteError(w, http.StatusBadRequest, "invalid login request")
		return
	}

	if err := h.logins.Request(r.Context(), req.Session); err != nil {
		if errors.Is(err, auth.ErrLoginUnknown) {
			wire.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}

		h.fail(w, r, err)

		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) loginToken(w http.ResponseWriter, r *http.Request) {
	tok, err := h.logins.Poll(r.Context(), r.PathValue("session"), "jf login")
	switch {
	case errors.Is(err, auth.ErrLoginPending):
		wire.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, auth.ErrLoginUnknown):
		wire.WriteError(w, http.StatusNotFound, err.Error())
	case err != nil:
		h.fail(w, r, err)
	default:
		wire.WriteJSON(w, http.StatusOK, Response(tok, RenderScope))
	}
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "request failed", "request_id", httpserver.RequestID(r.Context()), "error", err)
	wire.WriteError(w, http.StatusInternalServerError, "internal server error")
}
