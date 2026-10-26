package packages

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
	"relic/internal/platform/httpserver"
)

const (
	gitLFSMediaType     = "application/vnd.git-lfs+json"
	gitLFSLinkTTL       = time.Hour
	gitLFSMaxObjects    = 1000
	gitLFSMaxBatchBytes = 16 << 20
	gitLFSMaxObjectSize = 64 << 30
)

var gitLFSOid = regexp.MustCompile(`^[0-9a-f]{64}$`)

// RegisterGitLFS serves the Git LFS API (batch, basic transfers and the
// lock verification git-lfs push asks for) of Git LFS repositories at
// <prefix><repo>, the lfs.url git-lfs is given.
func (h *Handler) RegisterGitLFS(rt *httpserver.Router, m Mount) {
	rt.HandleFunc("POST "+m.Prefix+"{repo}/objects/batch", auth.Authenticated, mounted(m, h.gitLFSBatch))
	// The transfer links carry a signature of the batch call's caller.
	rt.HandleFunc("GET "+m.Prefix+"{repo}/objects/{oid}", auth.Public, h.gitLFSDownload)
	rt.HandleFunc("PUT "+m.Prefix+"{repo}/objects/{oid}", auth.Public, h.gitLFSUpload)
	rt.HandleFunc("POST "+m.Prefix+"{repo}/locks/verify", auth.Authenticated, h.gitLFSVerifyLocks)
	rt.HandleFunc("GET "+m.Prefix+"{repo}/locks", auth.Authenticated, h.gitLFSListLocks)
	rt.HandleFunc(m.Prefix, auth.Authenticated, h.gitLFSNotFound)
}

func gitLFSObjectPath(oid string) string {
	return "objects/" + oid[0:2] + "/" + oid[2:4] + "/" + oid
}

func gitLFSJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", gitLFSMediaType)
	w.WriteHeader(status)
	//nolint:errchkjson // client gone
	_ = json.NewEncoder(w).Encode(v)
}

func gitLFSError(w http.ResponseWriter, status int, message string) {
	gitLFSJSON(w, status, map[string]string{
		"message": message,
	})
}

func (h *Handler) gitLFSRepository(w http.ResponseWriter, r *http.Request) (repository.Repository, bool) {
	rp, err := h.repos.GetRepository(r.Context(), r.PathValue("repo"))
	if err != nil || rp.Format != repository.FormatGitLFS {
		gitLFSError(w, http.StatusNotFound, "Git LFS repository not found")
		return repository.Repository{}, false
	}

	return rp, true
}

type gitLFSBatchObject struct {
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
}

func (h *Handler) gitLFSBatch(w http.ResponseWriter, r *http.Request, m Mount) {
	rp, ok := h.gitLFSRepository(w, r)
	if !ok {
		return
	}

	release := h.buffers.reserve(w, r, gitLFSMaxBatchBytes, 2)
	if release == nil {
		return
	}
	defer release()

	var req struct {
		Operation string              `json:"operation"`
		Transfers []string            `json:"transfers"`
		Objects   []gitLFSBatchObject `json:"objects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, gitLFSMaxBatchBytes)).Decode(&req); err != nil {
		gitLFSError(w, http.StatusBadRequest, "invalid batch request")
		return
	}

	if len(req.Objects) > gitLFSMaxObjects {
		gitLFSError(
			w,
			http.StatusRequestEntityTooLarge,
			"a batch holds at most "+strconv.Itoa(gitLFSMaxObjects)+" objects",
		)

		return
	}

	if len(req.Transfers) > 0 && !slices.Contains(req.Transfers, "basic") {
		gitLFSError(w, http.StatusUnprocessableEntity, "only the basic transfer adapter is supported")
		return
	}

	repoKey, action := rp.Name, auth.ActionRead

	switch req.Operation {
	case "download":
	case "upload":
		target, err := h.repos.DeployTarget(r.Context(), rp.Name)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		repoKey, action = target, auth.ActionWrite
	default:
		gitLFSError(w, http.StatusUnprocessableEntity, "operation must be download or upload")
		return
	}

	if ok, err := h.CanRepoAnywhere(r, repoKey, action); err != nil || !ok {
		if err != nil {
			h.writeError(w, r, err)
		} else {
			gitLFSError(w, http.StatusForbidden, "missing '"+string(action)+"' permission on "+repoKey)
		}

		return
	}

	user := auth.FromContext(r.Context()).Name()

	objects := make([]map[string]any, 0, len(req.Objects))
	for _, o := range req.Objects {
		objects = append(objects, h.gitLFSBatchAnswer(r, m, repoKey, req.Operation, o, user))
	}

	gitLFSJSON(w, http.StatusOK, map[string]any{
		"transfer":  "basic",
		"objects":   objects,
		"hash_algo": "sha256",
	})
}

func (h *Handler) gitLFSBatchAnswer(
	r *http.Request,
	m Mount,
	repoKey, operation string,
	o gitLFSBatchObject,
	user string,
) map[string]any {
	answer := map[string]any{
		"oid":  o.Oid,
		"size": o.Size,
	}

	objectError := func(code int, message string) map[string]any {
		answer["error"] = map[string]any{
			"code":    code,
			"message": message,
		}

		return answer
	}

	if !gitLFSOid.MatchString(o.Oid) || o.Size < 0 || o.Size > gitLFSMaxObjectSize {
		return objectError(http.StatusUnprocessableEntity, "invalid object")
	}

	p := gitLFSObjectPath(o.Oid)

	action := auth.ActionRead
	if operation == "upload" {
		action = auth.ActionWrite
	}

	if ok, err := h.CanRepo(r, repoKey, p, action); err != nil || !ok {
		return objectError(http.StatusForbidden, "missing '"+string(action)+"' permission on "+repoKey+"/"+p)
	}

	var item repository.Item

	var err error
	if operation == "upload" {
		item, err = h.repos.Stat(r.Context(), repoKey, p)
	} else {
		item, err = h.repos.Resolve(r.Context(), repoKey, p)
	}

	switch {
	case err == nil && operation == "upload" && item.Size == o.Size:
		return answer
	case err == nil && operation == "download":
		answer["size"] = item.Size
	case operation == "download" && errors.Is(err, repository.ErrNotFound):
		return objectError(http.StatusNotFound, "object does not exist")
	case err != nil && !errors.Is(err, repository.ErrNotFound):
		h.log.WarnContext(r.Context(), "git lfs: look up object", "repo", repoKey, "error", err)
		return objectError(http.StatusInternalServerError, "object lookup failed")
	}

	link := m.Prefix + url.PathEscape(repoKey) + "/objects/" + o.Oid
	answer["authenticated"] = true
	answer["actions"] = map[string]any{
		operation: map[string]any{
			"href":       origin(r) + link + "?" + h.tokens.SignPath(link, user, gitLFSLinkTTL),
			"expires_in": int(gitLFSLinkTTL.Seconds()),
		},
	}

	return answer
}

func (h *Handler) gitLFSSigner(w http.ResponseWriter, r *http.Request) (string, bool) {
	q := r.URL.Query()

	user, err := h.tokens.VerifyPath(r.URL.EscapedPath(), q.Get("user"), q.Get("exp"), q.Get("kid"), q.Get("sig"))
	if err != nil {
		gitLFSError(w, http.StatusForbidden, "invalid or expired transfer link")
		return "", false
	}

	if !gitLFSOid.MatchString(r.PathValue("oid")) {
		gitLFSError(w, http.StatusNotFound, "object does not exist")
		return "", false
	}

	return user, true
}

func (h *Handler) gitLFSDownload(w http.ResponseWriter, r *http.Request) {
	user, ok := h.gitLFSSigner(w, r)
	if !ok {
		return
	}

	ctx := auth.WithPrincipal(r.Context(), auth.Principal{
		Username: user,
	})
	r = r.WithContext(ctx)

	item, err := h.repos.Resolve(ctx, r.PathValue("repo"), gitLFSObjectPath(r.PathValue("oid")))
	if errors.Is(err, repository.ErrNotFound) || err == nil && item.IsFolder() {
		gitLFSError(w, http.StatusNotFound, "object does not exist")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.allowDownload(w, r, item) {
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
	h.dockerStream(w, r, item)
}

func (h *Handler) gitLFSUpload(w http.ResponseWriter, r *http.Request) {
	user, ok := h.gitLFSSigner(w, r)
	if !ok {
		return
	}

	ctx := auth.WithPrincipal(r.Context(), auth.Principal{
		Username: user,
	})
	r = r.WithContext(ctx)

	oid := r.PathValue("oid")

	_, err := h.repos.Deploy(ctx, repository.DeployInput{
		Repository: r.PathValue("repo"),
		Path:       gitLFSObjectPath(oid),
		Body:       http.MaxBytesReader(w, r.Body, gitLFSMaxObjectSize),
		Expected: repository.Checksums{
			SHA256: oid,
		},
		User: user,
	})
	if errors.Is(err, repository.ErrChecksumMismatch) {
		gitLFSError(w, http.StatusBadRequest, "the content does not match the object id")
		return
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) gitLFSVerifyLocks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.gitLFSRepository(w, r); !ok {
		return
	}

	gitLFSJSON(w, http.StatusOK, map[string]any{
		"ours":   []any{},
		"theirs": []any{},
	})
}

func (h *Handler) gitLFSListLocks(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.gitLFSRepository(w, r); !ok {
		return
	}

	gitLFSJSON(w, http.StatusOK, map[string]any{
		"locks": []any{},
	})
}

func (h *Handler) gitLFSNotFound(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/locks") {
		gitLFSError(w, http.StatusNotImplemented, "Relic does not support Git LFS file locking")
		return
	}

	gitLFSError(w, http.StatusNotFound, "not found")
}
