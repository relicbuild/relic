package packages

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/npm"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httperror"
)

const maxPublishBytes = 512 << 20

type npmRequest struct {
	mount Mount
	repo  string
	rest  string
}

// Scoped names arrive as "@scope%2fname" (packuments) or "@scope/name" (tarballs).
func npmPackage(rest string) (name, tail string, err error) {
	segs := strings.Split(rest, "/")

	n := 1
	if strings.HasPrefix(segs[0], "@") && !strings.Contains(strings.ToLower(segs[0]), "%2f") && len(segs) > 1 {
		n = 2
	}

	raw := strings.Join(segs[:min(n, len(segs))], "/")
	if name, err = url.PathUnescape(raw); err != nil {
		return "", "", err
	}

	return name, strings.Join(segs[min(n, len(segs)):], "/"), nil
}

func (h *Handler) npmAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	esc := strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix)
	repoKey, rest, _ := strings.Cut(esc, "/")

	req := npmRequest{
		mount: m,
		repo:  repoKey,
		rest:  rest,
	}
	switch {
	case rest == "-/whoami":
		writeJSON(w, http.StatusOK, map[string]string{
			"username": auth.FromContext(r.Context()).Name(),
		})
	case strings.HasPrefix(rest, "-/user/org.couchdb.user:") && r.Method == http.MethodPut:
		h.npmLogin(w, r)
	case strings.HasPrefix(rest, "-/user/token/") && r.Method == http.MethodDelete:
		h.npmLogout(w, r, strings.TrimPrefix(rest, "-/user/token/"))
	case strings.HasPrefix(rest, "-/npm/v1/security/") && r.Method == http.MethodPost:
		h.npmAudit(w, r, req, rest)
	case rest == "-/npm/v1/keys" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.npmSigningKeys(w, r, req)
	case strings.HasPrefix(rest, "-/npm/v1/attestations/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		h.npmAttestations(w, r, req, strings.TrimPrefix(rest, "-/npm/v1/attestations/"))
	case rest == "-/v1/search" && r.Method == http.MethodGet:
		h.npmSearch(w, r, req)
	case strings.HasPrefix(rest, "-/package/"):
		h.npmDistTags(w, r, req, strings.TrimPrefix(rest, "-/package/"))
	default:
		h.npmPackageRequest(w, r, req)
	}
}

func (h *Handler) npmPackageRequest(w http.ResponseWriter, r *http.Request, req npmRequest) {
	name, tail, err := npmPackage(req.rest)
	if err != nil || !npm.ValidName(name) {
		httperror.Write(w, r, http.StatusBadRequest, "invalid package name")
		return
	}

	switch {
	case tail == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		if !h.RequireRepo(w, r, RepoCheck{
			req.repo,
			name,
			auth.ActionRead,
		}) {
			return
		}

		form := npmDocumentForm(r.Header.Values("Accept"))

		body, err := h.npm.Document(r.Context(), req.repo, name, req.mount.base(r)+"/"+req.repo, form)
		if err != nil {
			h.npmError(w, r, err)
			return
		}

		w.Header().Add("Vary", "Accept")

		if form == npm.FormAbbreviated {
			w.Header().Set("Content-Type", npm.AbbreviatedMediaType)
		} else {
			w.Header().Set("Content-Type", "application/json")
		}

		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	case tail == "" && r.Method == http.MethodPut:
		h.npmPublish(w, r, req, name)
	case strings.HasPrefix(tail, "-rev/") && (r.Method == http.MethodPut || r.Method == http.MethodDelete):
		h.npmUnpublish(w, r, req, name)
	case strings.HasPrefix(tail, "-/") && strings.Contains(tail, "/-rev/") && r.Method == http.MethodDelete:
		file, _, _ := strings.Cut(strings.TrimPrefix(tail, "-/"), "/-rev/")
		h.npmDeleteTarball(w, r, req, name, file)
	case strings.HasPrefix(tail, "-/") && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		// Scoped local tarballs repeat the scope: @s/p/-/@s/p-1.0.0.tgz.
		file, err := url.PathUnescape(strings.TrimPrefix(tail, "-/"))
		if err != nil {
			httperror.Write(w, r, http.StatusBadRequest, "invalid tarball name")
			return
		}

		h.ServeFile(w, r, req.mount, Target{
			Repo: req.repo,
			Path: name + "/-/" + file,
		})
	default:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "unsupported npm request")
	}
}

func npmDocumentForm(accept []string) npm.Form {
	var abbreviated, full float64

	for _, header := range accept {
		for mediaRange := range strings.SplitSeq(header, ",") {
			mediaType, params, err := mime.ParseMediaType(mediaRange)
			if err != nil {
				continue
			}

			quality := 1.0
			if q, ok := params["q"]; ok {
				if quality, err = strconv.ParseFloat(q, 64); err != nil {
					continue
				}
			}

			switch mediaType {
			case npm.AbbreviatedMediaType:
				abbreviated = max(abbreviated, quality)
			case "application/json":
				full = max(full, quality)
			}
		}
	}

	if abbreviated > 0 && abbreviated >= full {
		return npm.FormAbbreviated
	}

	return npm.FormFull
}

func (h *Handler) npmPublish(w http.ResponseWriter, r *http.Request, req npmRequest, name string) {
	dest, err := h.repos.DeployTarget(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		dest,
		name,
		auth.ActionWrite,
	}) {
		return
	}

	release := reserveBuffer(w, r, maxPublishBytes, 2)
	if release == nil {
		return
	}
	defer release()

	var doc npm.PublishDocument
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishBytes)).Decode(&doc); err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "invalid publish document")
		return
	}

	if doc.Name == "" {
		doc.Name = name
	}

	if doc.Name != name {
		httperror.Write(w, r, http.StatusBadRequest, "package name does not match the URL")
		return
	}

	if len(doc.Attachments) == 0 {
		if err := h.npm.UpdateMetadata(r.Context(), dest, doc); err != nil {
			h.npmError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			"id": name,
		})

		return
	}

	if err := h.npm.Publish(r.Context(), dest, doc, auth.FromContext(r.Context()).Name()); err != nil {
		h.npmError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"ok": true,
		"id": name,
	})
}

func (h *Handler) npmUnpublish(w http.ResponseWriter, r *http.Request, req npmRequest, name string) {
	dest, err := h.repos.DeployTarget(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		dest,
		name,
		auth.ActionDelete,
	}) {
		return
	}

	var (
		doc  npm.PublishDocument
		keep func(version string) bool
	)

	if r.Method == http.MethodPut {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublishBytes)).Decode(&doc); err != nil ||
			doc.Name != name || len(doc.Versions) == 0 {
			httperror.Write(w, r, http.StatusBadRequest, "the packument must name the package and keep a version")
			return
		}

		keep = func(version string) bool {
			_, kept := doc.Versions[version]
			return kept
		}
	}

	user := auth.FromContext(r.Context()).Name()
	if err := h.npm.Unpublish(r.Context(), dest, name, user, keep); err != nil {
		h.npmError(w, r, err)
		return
	}

	for tag, version := range doc.DistTags {
		if err := h.npm.SetTag(r.Context(), dest, name, tag, version); err != nil && !errors.Is(err, npm.ErrNotFound) {
			h.npmError(w, r, err)
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}

func (h *Handler) npmDeleteTarball(w http.ResponseWriter, r *http.Request, req npmRequest, name, escapedFile string) {
	file, err := url.PathUnescape(escapedFile)
	if err != nil || file == "" || strings.Contains(file, "..") {
		httperror.Write(w, r, http.StatusBadRequest, "invalid tarball name")
		return
	}

	dest, err := h.repos.DeployTarget(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	p := name + "/-/" + file
	if !h.RequireRepo(w, r, RepoCheck{
		dest,
		p,
		auth.ActionDelete,
	}) {
		return
	}

	if err := h.repos.DeleteToTrash(r.Context(), dest, p, auth.FromContext(r.Context()).Name()); err != nil {
		h.npmError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}

func (h *Handler) npmDistTags(w http.ResponseWriter, r *http.Request, req npmRequest, rest string) {
	name, tail, err := npmPackage(rest)
	if err != nil || !npm.ValidName(name) || !strings.HasPrefix(tail, "dist-tags") {
		httperror.Write(w, r, http.StatusBadRequest, "invalid dist-tags request")
		return
	}

	tag, _ := url.PathUnescape(strings.TrimPrefix(strings.TrimPrefix(tail, "dist-tags"), "/"))

	ctx := r.Context()
	switch {
	case r.Method == http.MethodGet && tag == "":
		if !h.RequireRepo(w, r, RepoCheck{
			req.repo,
			name,
			auth.ActionRead,
		}) {
			return
		}

		tags, err := h.npm.DistTags(ctx, req.repo, name)
		if err != nil {
			h.npmError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, tags)
	case r.Method == http.MethodPut && tag != "":
		dest, err := h.repos.DeployTarget(ctx, req.repo)
		if err != nil {
			h.npmError(w, r, err)
			return
		}

		req.repo = dest
		if !h.RequireRepo(w, r, RepoCheck{
			req.repo,
			name,
			auth.ActionWrite,
		}) {
			return
		}

		var version string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&version); err != nil {
			httperror.Write(w, r, http.StatusBadRequest, "body must be a JSON version string")
			return
		}

		if err := h.npm.SetTag(ctx, req.repo, name, tag, version); err != nil {
			h.npmError(w, r, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]bool{
			"ok": true,
		})
	case r.Method == http.MethodDelete && tag != "":
		dest, err := h.repos.DeployTarget(ctx, req.repo)
		if err != nil {
			h.npmError(w, r, err)
			return
		}

		req.repo = dest
		if !h.RequireRepo(w, r, RepoCheck{
			req.repo,
			name,
			auth.ActionDelete,
		}) {
			return
		}

		if err := h.npm.DeleteTag(ctx, req.repo, name, tag); err != nil {
			h.npmError(w, r, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]bool{
			"ok": true,
		})
	default:
		httperror.Write(w, r, http.StatusMethodNotAllowed, "unsupported dist-tags request")
	}
}

func (h *Handler) npmSearch(w http.ResponseWriter, r *http.Request, req npmRequest) {
	if !h.RequireRepo(w, r, RepoCheck{
		req.repo,
		"",
		auth.ActionRead,
	}) {
		return
	}

	size := int32(20)
	if n, err := strconv.ParseInt(r.URL.Query().Get("size"), 10, 32); err == nil && n > 0 && n <= 250 {
		size = int32(n)
	}

	keys := []string{
		req.repo,
	}
	if rp, err := h.repos.GetRepository(r.Context(), req.repo); err == nil && rp.Class == repository.ClassVirtual {
		keys = rp.Members()
	}

	hits, err := h.npm.Search(r.Context(), keys, r.URL.Query().Get("text"), size)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	objects := make([]map[string]any, len(hits))
	for i, hit := range hits {
		objects[i] = map[string]any{
			"package": npmSearchPackage(hit),
			"score": map[string]any{
				"final": 1,
				"detail": map[string]any{
					"quality":     1,
					"popularity":  1,
					"maintenance": 1,
				},
			},
		}
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]any{
			"objects": objects,
			"total":   len(objects),
			"time":    time.Now().UTC().Format(time.RFC1123),
		},
	)
}

type npmPerson struct {
	Username string `json:"username"`
	Email    string `json:"email,omitempty"`
}

func npmSearchPackage(hit npm.SearchResult) map[string]any {
	maintainers := make([]npmPerson, 0, len(hit.Maintainers))
	for _, m := range hit.Maintainers {
		maintainers = append(maintainers, npmPerson{
			Username: m.Name,
			Email:    m.Email,
		})
	}

	pkg := map[string]any{
		"name":        hit.Name,
		"version":     hit.Version,
		"description": hit.Description,
		"keywords":    hit.Keywords,
		"maintainers": maintainers,
		"date":        hit.Date.UTC().Format(time.RFC3339),
	}
	if hit.Publisher != "" {
		pkg["publisher"] = npmPerson{
			Username: hit.Publisher,
		}
	}

	return pkg
}

func (h *Handler) npmLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || body.Name == "" {
		httperror.Write(w, r, http.StatusBadRequest, "invalid login request")
		return
	}

	p, err := h.authn.Password(r, body.Name, body.Password)
	if err != nil {
		if errors.Is(err, auth.ErrBadCredentials) {
			httperror.Write(w, r, http.StatusUnauthorized, "Bad credentials")
			return
		}

		if auth.WriteThrottled(w, r, err) {
			return
		}

		h.writeError(w, r, err)

		return
	}

	tok, err := h.tokens.Issue(r.Context(), p.LimitIssue(auth.IssueParams{
		Username:    p.Username,
		Scope:       auth.UserScope,
		ExpiresIn:   npmLoginTokenLifetime,
		Description: "npm login",
	}, time.Now()))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeJSON(
		w,
		http.StatusCreated,
		map[string]any{
			"ok":    true,
			"id":    "org.couchdb.user:" + body.Name,
			"token": tok.AccessToken,
		},
	)
}

const npmLoginTokenLifetime = 90 * 24 * time.Hour

func (h *Handler) npmLogout(w http.ResponseWriter, r *http.Request, escaped string) {
	caller := auth.FromContext(r.Context())
	if caller.Anonymous() {
		httperror.Write(w, r, http.StatusUnauthorized, "authentication required")
		return
	}

	token, err := url.PathUnescape(escaped)
	if err != nil {
		httperror.Write(w, r, http.StatusNotFound, "Not Found")
		return
	}

	tok, err := h.tokens.Verify(r.Context(), token)
	if errors.Is(err, auth.ErrInvalidToken) || err == nil && tok.Username != caller.Username {
		httperror.Write(w, r, http.StatusNotFound, "Not Found")
		return
	}

	if err == nil {
		err = h.tokens.RevokeID(r.Context(), tok.ID, caller.Username)
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{
		"ok": true,
	})
}

func (h *Handler) npmError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, npm.ErrNotFound), errors.Is(err, repository.ErrNotFound):
		httperror.Write(w, r, http.StatusNotFound, "Not Found")
	case errors.Is(err, npm.ErrVersionExists):
		// The npm registry answers 403 for republishing a version.
		httperror.Write(w, r, http.StatusForbidden, err.Error())
	case errors.Is(err, npm.ErrInvalid):
		httperror.Write(w, r, http.StatusBadRequest, err.Error())
	default:
		h.writeError(w, r, err)
	}
}

func (h *Handler) npmSigningKeys(w http.ResponseWriter, r *http.Request, req npmRequest) {
	if !h.RequireRepo(w, r, RepoCheck{
		req.repo,
		"",
		auth.ActionRead,
	}) {
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	keys, err := h.npm.SigningKeys(r.Context(), rp)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	writeRegistryDocument(w, keys)
}

func (h *Handler) npmAttestations(w http.ResponseWriter, r *http.Request, req npmRequest, spec string) {
	unescaped, err := url.PathUnescape(spec)
	at := strings.LastIndex(unescaped, "@")

	if err != nil || at <= 0 {
		httperror.Write(w, r, http.StatusBadRequest, "invalid package version")
		return
	}

	name, version := unescaped[:at], unescaped[at+1:]
	if !h.RequireRepo(w, r, RepoCheck{
		req.repo,
		name,
		auth.ActionRead,
	}) {
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	attestations, err := h.npm.Attestations(r.Context(), rp, name, version)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	writeRegistryDocument(w, attestations)
}

func writeRegistryDocument(w http.ResponseWriter, document []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(document) //nolint:gosec // G705: the upstream registry's JSON, served as JSON
}

func (h *Handler) npmAudit(w http.ResponseWriter, r *http.Request, req npmRequest, apiPath string) {
	if !h.RequireRepo(w, r, RepoCheck{
		req.repo,
		"",
		auth.ActionRead,
	}) {
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), req.repo)
	if err != nil {
		h.npmError(w, r, err)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, npm.MaxAuditBody))
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		httperror.Write(w, r, http.StatusRequestEntityTooLarge, "the audit request is too large")
		return
	}

	if err != nil {
		httperror.Write(w, r, http.StatusBadRequest, "the audit request could not be read")
		return
	}

	resp, err := h.npm.Audit(r.Context(), rp, apiPath, bytes.NewReader(body), r.Header)
	if err != nil {
		h.log.WarnContext(r.Context(), "npm audit unavailable", "repo", req.repo, "error", err)
		httperror.Write(w, r, http.StatusServiceUnavailable, "npm audit is not available for this repository")

		return
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(
		w,
		io.LimitReader(resp.Body, 64<<20),
	) //nolint:gosec // G704/G705: the upstream's audit report, relayed with its content type
}
