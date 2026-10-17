package access

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"relic/internal/access/workload"
)

// SetWorkloads exchanges OIDC tokens through w.
func (h *Handler) SetWorkloads(w *workload.Service) { h.workloads = w }

type oidcTokenRequest struct {
	GrantType        string `json:"grant_type"`
	SubjectTokenType string `json:"subject_token_type"`
	SubjectToken     string `json:"subject_token"`
	ProviderName     string `json:"provider_name"`
}

const accessTokenType = "urn:ietf:params:oauth:token-type:access_token" //nolint:gosec // G101: a token type URN

func (h *Handler) exchangeOIDCToken(w http.ResponseWriter, r *http.Request) {
	if h.workloads == nil {
		accessError(w, http.StatusNotFound, "OIDC token exchange is not available on this server")
		return
	}

	var in oidcTokenRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		accessError(w, http.StatusBadRequest, "invalid token exchange request")
		return
	}

	if in.SubjectToken == "" || in.ProviderName == "" {
		accessError(w, http.StatusBadRequest, "subject_token and provider_name are required")
		return
	}

	got, err := h.workloads.Exchange(r.Context(), in.ProviderName, in.SubjectToken)
	if errors.Is(err, workload.ErrRefused) {
		h.log.InfoContext(r.Context(), "oidc token exchange refused", "provider", in.ProviderName, "error", err)
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"errors": []map[string]string{
				{
					"code":    "UNAUTHORIZED",
					"message": "the token exchange was refused",
				},
			},
		})

		return
	}

	if err != nil {
		h.log.ErrorContext(r.Context(), "oidc token exchange", "provider", in.ProviderName, "error", err)
		accessError(w, http.StatusInternalServerError, "the token exchange failed")

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":      got.Token.AccessToken,
		"token_type":        "Bearer",
		"expires_in":        int64(got.Token.ExpiresIn / time.Second),
		"scope":             RenderScope(got.Token.Scope),
		"issued_token_type": accessTokenType,
		"username":          got.User,
	})
}
