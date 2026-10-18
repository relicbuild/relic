package packages

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"

	hf "relic/internal/artifacts/formats/huggingface"
	"relic/internal/artifacts/formats/huggingface/xet"
)

const (
	hfXetTokenPrefix = "xet_"
	hfXetTokenTTL    = time.Hour
	hfXetCASPath     = "/api/xet"
	hfXetAccessRead  = "read"
	hfXetAccessWrite = "write"
)

// hfXetScope is what a Xet token grants: read or write access to the CAS
// of one repository, for one Hub repository and revision.
type hfXetScope struct {
	repository string
	hubPath    string
	revision   string
	access     string
}

// signedPath is the text the token's signature covers.
func (s hfXetScope) signedPath() string {
	return strings.Join(
		[]string{
			"huggingface-xet",
			s.repository,
			s.hubPath,
			s.revision,
			s.access,
		},
		"\n",
	)
}

func (h *Handler) hfXetToken(scope hfXetScope, user string) (token string, expires int64, err error) {
	values, err := url.ParseQuery(h.tokens.SignPath(scope.signedPath(), user, hfXetTokenTTL))
	if err != nil {
		return "", 0, err
	}

	expires, err = strconv.ParseInt(values.Get("exp"), 10, 64)
	if err != nil {
		return "", 0, err
	}

	values.Set("repo", scope.repository)
	values.Set("path", scope.hubPath)
	values.Set("rev", scope.revision)
	values.Set("access", scope.access)

	return hfXetTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(values.Encode())), expires, nil
}

// hfXetPrincipal verifies the Xet token of a CAS request and returns its
// scope and user; it writes the error and returns false when the token is
// missing, invalid, for another repository or lacks write access.
func (h *Handler) hfXetPrincipal(w http.ResponseWriter, r *http.Request, write bool) (hfXetScope, string, bool) {
	bearer, _ := auth.BearerToken(r.Header.Get("Authorization"))

	encoded, ok := strings.CutPrefix(bearer, hfXetTokenPrefix)
	if !ok {
		hfError(w, http.StatusUnauthorized, "", "a Xet access token is required")
		return hfXetScope{}, "", false
	}

	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		hfError(w, http.StatusUnauthorized, "", "invalid Xet access token")
		return hfXetScope{}, "", false
	}

	values, err := url.ParseQuery(string(raw))
	if err != nil {
		hfError(w, http.StatusUnauthorized, "", "invalid Xet access token")
		return hfXetScope{}, "", false
	}

	scope := hfXetScope{
		repository: values.Get("repo"),
		hubPath:    values.Get("path"),
		revision:   values.Get("rev"),
		access:     values.Get("access"),
	}

	user, err := h.tokens.VerifyPath(
		scope.signedPath(),
		values.Get("user"),
		values.Get("exp"),
		values.Get("kid"),
		values.Get("sig"),
	)
	if err != nil || scope.repository != r.PathValue("repo") {
		hfError(w, http.StatusUnauthorized, "", "invalid or expired Xet access token")
		return hfXetScope{}, "", false
	}

	if write && scope.access != hfXetAccessWrite {
		hfError(w, http.StatusForbidden, "", "the Xet access token is read-only")
		return hfXetScope{}, "", false
	}

	return scope, user, true
}

// hfXetTokenIssue answers xet-read-token and xet-write-token: a Xet access
// token for the repository's CAS, in the headers huggingface_hub reads and
// the JSON body hf_xet reads.
func (h *Handler) hfXetTokenIssue(
	w http.ResponseWriter,
	r *http.Request,
	m Mount,
	rp repository.Repository,
	kind hf.Kind,
	id, op, rev string,
) {
	scope := hfXetScope{
		repository: rp.Name,
		hubPath:    string(kind) + "/" + id,
		revision:   rev,
		access:     hfXetAccessRead,
	}

	if op == "xet-write-token" {
		target, ok := h.hfUploadTarget(w, r, rp, scope.hubPath)
		if !ok {
			return
		}

		scope.repository, scope.access = target, hfXetAccessWrite
	} else if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		scope.hubPath,
		auth.ActionRead,
	}) {
		return
	}

	token, expires, err := h.hfXetToken(scope, auth.FromContext(r.Context()).Name())
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	casURL := origin(r) + m.Prefix + url.PathEscape(scope.repository) + hfXetCASPath

	w.Header().Set("X-Xet-Cas-Url", casURL)
	w.Header().Set("X-Xet-Access-Token", token)
	w.Header().Set("X-Xet-Token-Expiration", strconv.FormatInt(expires, 10))
	writeJSON(w, http.StatusOK, map[string]any{
		"casUrl":      casURL,
		"accessToken": token,
		"exp":         expires,
	})
}

func (h *Handler) hfXetFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, xet.ErrInvalid):
		hfError(w, http.StatusBadRequest, "", err.Error())
	default:
		h.writeError(w, r, err)
	}
}

func hfXetHash(w http.ResponseWriter, r *http.Request) (xet.Hash, bool) {
	if r.PathValue("prefix") != "default" {
		hfError(w, http.StatusBadRequest, "", "unknown xorb prefix")
		return xet.Hash{}, false
	}

	hash, err := xet.ParseHash(r.PathValue("hash"))
	if err != nil {
		hfError(w, http.StatusBadRequest, "", err.Error())
		return xet.Hash{}, false
	}

	return hash, true
}

// The Xet token's write access was checked when it was issued.
func (h *Handler) hfXetUploadXorb(w http.ResponseWriter, r *http.Request) {
	scope, user, ok := h.hfXetPrincipal(w, r, true)
	if !ok {
		return
	}

	hash, ok := hfXetHash(w, r)
	if !ok {
		return
	}

	if r.ContentLength > xet.MaxXorbSize {
		hfError(w, http.StatusRequestEntityTooLarge, "", "a xorb holds at most "+strconv.Itoa(xet.MaxXorbSize)+" bytes")
		return
	}

	inserted, err := h.hf.StoreXorb(r.Context(), scope.repository, hash, r.Body, user)
	if err != nil {
		h.hfXetFail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"was_inserted": inserted,
	})
}

func (h *Handler) hfXetHeadXorb(w http.ResponseWriter, r *http.Request) {
	scope, _, ok := h.hfXetPrincipal(w, r, false)
	if !ok {
		return
	}

	hash, ok := hfXetHash(w, r)
	if !ok {
		return
	}

	size, err := h.hf.XorbSize(r.Context(), scope.repository, hash)
	if errors.Is(err, repository.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if err != nil {
		h.hfXetFail(w, r, err)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
}

// The Xet token's write access was checked when it was issued.
func (h *Handler) hfXetUploadShard(w http.ResponseWriter, r *http.Request) {
	scope, user, ok := h.hfXetPrincipal(w, r, true)
	if !ok {
		return
	}

	if r.ContentLength > xet.MaxShardSize {
		hfError(
			w,
			http.StatusRequestEntityTooLarge,
			"",
			"a shard holds at most "+strconv.Itoa(xet.MaxShardSize)+" bytes",
		)

		return
	}

	if err := h.hf.RegisterShard(r.Context(), scope.repository, r.Body, user); err != nil {
		h.hfXetFail(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]int{
		"result": 1,
	})
}

// hfXetNotServed answers the CAS endpoints Relic does not serve (global
// deduplication, streamed shard uploads, reconstructions) with the 404
// clients fall back on.
func (h *Handler) hfXetNotServed(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := h.hfXetPrincipal(w, r, false); !ok {
		return
	}

	hfError(w, http.StatusNotFound, "", "not found")
}
