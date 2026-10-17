package packages

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"

	hf "relic/internal/artifacts/formats/huggingface"
)

const (
	hfMaxCommit          = 512 << 20
	hfLFSUploadTTL       = time.Hour
	hfLFSBatchMaxObjects = 1000
)

func (h *Handler) hfAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != hf.Format {
		hfError(w, http.StatusNotFound, "RepoNotFound", "repository not found")
		return
	}

	parts := strings.Split(rest, "/")
	switch {
	case rest == "api/validate-yaml" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"errors":   []string{},
			"warnings": []string{},
		})
	case rest == "api/repos/create" && r.Method == http.MethodPost:
		h.hfCreateRepo(w, r, m, rp)
	case len(parts) >= 4 && parts[0] == "api":
		kind, ok := hf.ParseKind(parts[1])

		id, op, arg, found := hfSplit(parts[2:], "revision", "tree", "preupload", "commit")
		if !ok || !found {
			hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
			return
		}

		h.hfRepoAPI(w, r, m, rp, kind, id, op, arg)
	case strings.HasSuffix(rest, ".git/info/lfs/objects/batch"):
		h.hfLFSBatch(w, r, m, rp)
	default:
		kind := hf.Kind("models")

		p := parts
		if p[0] == "datasets" {
			kind, p = "datasets", p[1:]
		}

		id, _, arg, found := hfSplit(p, "resolve")
		if !found || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
			return
		}

		rev, file, _ := strings.Cut(arg, "/")
		h.hfResolve(w, r, rp, kind, id, rev, file)
	}
}

func hfSplit(p []string, ops ...string) (id, op, rest string, ok bool) {
	for n := 1; n <= 2 && n < len(p); n++ {
		for _, o := range ops {
			if p[n] == o {
				id = strings.Join(p[:n], "/")
				if !hf.ValidID(id) {
					return "", "", "", false
				}

				return id, o, strings.Join(p[n+1:], "/"), true
			}
		}
	}

	return "", "", "", false
}

func hfError(w http.ResponseWriter, status int, code, msg string) {
	if code != "" {
		w.Header().Set("X-Error-Code", code)
	}

	w.Header().Set("X-Error-Message", msg)
	writeJSON(w, status, map[string]string{
		"error": msg,
	})
}

func (h *Handler) hfFail(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, hf.ErrRepoNotFound):
		hfError(w, http.StatusNotFound, "RepoNotFound", err.Error())
	case errors.Is(err, hf.ErrRevisionNotFound):
		hfError(w, http.StatusNotFound, "RevisionNotFound", err.Error())
	case errors.Is(err, hf.ErrEntryNotFound), errors.Is(err, repository.ErrNotFound):
		hfError(w, http.StatusNotFound, "EntryNotFound", "entry not found")
	case errors.Is(err, hf.ErrInvalid), errors.Is(err, repository.ErrChecksumMismatch):
		hfError(w, http.StatusBadRequest, "", err.Error())
	default:
		h.writeError(w, r, err)
	}

	return true
}

func (h *Handler) hfRepoAPI(
	w http.ResponseWriter,
	r *http.Request,
	m Mount,
	rp repository.Repository,
	kind hf.Kind,
	id, op, rev string,
) {
	switch {
	case (op == "revision" || op == "tree") && r.Method == http.MethodGet:
		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			string(kind) + "/" + id,
			auth.ActionRead,
		}) {
			return
		}

		h.hfRevision(w, r, rp, kind, id, rev, op == "tree")
	case op == "preupload" && r.Method == http.MethodPost:
		h.hfPreupload(w, r, rp)
	case op == "commit" && r.Method == http.MethodPost:
		h.hfCommit(w, r, m, rp, kind, id, rev)
	default:
		hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
	}
}

func (h *Handler) hfMembers(r *http.Request, rp repository.Repository) []repository.Repository {
	if rp.Class != "virtual" {
		return []repository.Repository{
			rp,
		}
	}

	var out []repository.Repository

	for _, m := range h.ReadRepos(r.Context(), rp) {
		if m.Class != "virtual" {
			out = append(out, m)
		}
	}

	return out
}

func (h *Handler) hfRevision(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	kind hf.Kind,
	id, rev string,
	tree bool,
) {
	lastErr := error(hf.ErrRepoNotFound)

	for _, m := range h.hfMembers(r, rp) {
		if m.Class == "remote" {
			var body any

			info, err := h.hf.RemoteInfo(r.Context(), m, kind, id, rev)
			if err == nil {
				body = info
			}

			if err == nil && tree {
				sha, _ := info["sha"].(string)
				body, err = h.hf.RemoteTree(r.Context(), m, kind, id, sha)
			}

			if err != nil {
				lastErr = err
				continue
			}

			writeJSON(w, http.StatusOK, body)

			return
		}

		c, err := h.hf.Revision(r.Context(), m.Name, kind, id, rev)
		if err != nil {
			lastErr = err
			continue
		}

		files, err := h.hf.Files(r.Context(), m.Name, c)
		if h.hfFail(w, r, err) {
			return
		}

		if !tree {
			writeJSON(w, http.StatusOK, hf.Info(c, files))
			return
		}

		entries := make([]map[string]any, 0, len(files))
		for _, f := range files {
			entries = append(
				entries,
				map[string]any{
					"type": "file",
					"path": f.Path,
					"size": f.Item.Size,
					"oid":  f.Item.Checksums.SHA1,
				},
			)
		}

		writeJSON(w, http.StatusOK, entries)

		return
	}

	h.hfFail(w, r, lastErr)
}

// The HEAD must carry X-Repo-Commit, ETag and Content-Length.
func (h *Handler) hfResolve(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	kind hf.Kind,
	id, rev, file string,
) {
	if file == "" {
		hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
		return
	}

	if !h.RequireRepo(w, r, RepoCheck{
		rp.Name,
		string(kind) + "/" + id,
		auth.ActionRead,
	}) {
		return
	}

	lastErr := error(hf.ErrRepoNotFound)

	for _, m := range h.hfMembers(r, rp) {
		var item repository.Item

		sha, err := rev, error(nil)
		if m.Class == "remote" {
			if !hf.IsCommitSHA(rev) {
				sha, err = h.hf.RemoteSHA(r.Context(), m, kind, id, rev)
			}

			if err == nil {
				item, err = h.hf.RemoteFile(r.Context(), m, kind, id, sha, file)
			}
		} else {
			var c hf.Commit
			if c, err = h.hf.Revision(r.Context(), m.Name, kind, id, rev); err == nil {
				sha = c.Oid
				item, err = h.repos.Stat(r.Context(), m.Name, c.Dir()+"/"+file)
			}
		}

		if err != nil || item.IsFolder() {
			if err == nil {
				err = hf.ErrEntryNotFound
			}

			lastErr = err

			continue
		}

		if !h.allowDownload(w, r, item) {
			return
		}

		w.Header().Set("X-Repo-Commit", sha)
		w.Header().Set("ETag", `"`+item.Checksums.SHA256+`"`)
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")

		rng, ok := parseRange(r.Header.Get("Range"), item.Size)
		if !ok {
			w.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(item.Size, 10))
			hfError(w, http.StatusRequestedRangeNotSatisfiable, "", "requested range not satisfiable")

			return
		}

		status, length := http.StatusOK, item.Size
		if rng != nil {
			status, length = http.StatusPartialContent, rng.End-rng.Start+1
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", rng.Start, rng.End, item.Size))
		}

		w.Header().Set("Content-Length", strconv.FormatInt(length, 10))

		if r.Method == http.MethodHead || length == 0 {
			w.WriteHeader(status)
			return
		}

		body, err := h.repos.Open(r.Context(), item, rng)
		if err != nil {
			w.Header().Del("Content-Length")
			h.hfFail(w, r, err)

			return
		}

		defer func() {
			_ = body.Close()
		}()

		w.WriteHeader(status)

		if _, err := copyBody(w, body); err != nil {
			panic(http.ErrAbortHandler)
		}

		return
	}

	h.hfFail(w, r, lastErr)
}

func (h *Handler) hfCreateRepo(w http.ResponseWriter, r *http.Request, m Mount, rp repository.Repository) {
	var req struct {
		Name         string `json:"name"`
		Organization string `json:"organization"`
		Type         string `json:"type"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		hfError(w, http.StatusBadRequest, "BadRequest", "invalid request body")
		return
	}

	var kind hf.Kind

	switch req.Type {
	case "", "model":
		kind = "models"
	case "dataset":
		kind = "datasets"
	default:
		hfError(w, http.StatusBadRequest, "BadRequest", "only model and dataset repositories are supported")
		return
	}

	id := req.Name
	if req.Organization != "" {
		id = req.Organization + "/" + req.Name
	}

	if !hf.ValidID(id) {
		hfError(w, http.StatusBadRequest, "BadRequest", "invalid repository name")
		return
	}

	if _, ok := h.hfUploadTarget(w, r, rp, string(kind)+"/"+id); !ok {
		return
	}

	path := id
	if kind == "datasets" {
		path = "datasets/" + id
	}

	writeJSON(
		w,
		http.StatusOK,
		map[string]any{
			"url":  m.base(r) + "/" + rp.Name + "/" + path,
			"name": id,
		},
	)
}

func (h *Handler) hfUploadTarget(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	p string,
) (string, bool) {
	target, err := h.repos.DeployTarget(r.Context(), rp.Name)
	if err != nil {
		h.writeError(w, r, err)
		return "", false
	}

	return target, h.RequireRepo(w, r, RepoCheck{
		target,
		p,
		auth.ActionWrite,
	})
}

func (h *Handler) hfPreupload(w http.ResponseWriter, r *http.Request, rp repository.Repository) {
	if _, ok := h.hfUploadTarget(w, r, rp, ""); !ok {
		return
	}

	var req struct {
		Files []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&req); err != nil {
		hfError(w, http.StatusBadRequest, "", "invalid preupload body")
		return
	}

	files := make([]map[string]any, 0, len(req.Files))
	for _, f := range req.Files {
		mode := "regular"
		if f.Size > hf.LFSThreshold {
			mode = "lfs"
		}

		files = append(files, map[string]any{
			"path":         f.Path,
			"uploadMode":   mode,
			"shouldIgnore": false,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"files": files,
	})
}

// huggingface_hub sends those PUTs without credentials.
func (h *Handler) hfLFSBatch(w http.ResponseWriter, r *http.Request, m Mount, rp repository.Repository) {
	if r.Method != http.MethodPost {
		hfError(w, http.StatusMethodNotAllowed, "", "method not allowed")
		return
	}

	target, ok := h.hfUploadTarget(w, r, rp, "")
	if !ok {
		return
	}

	var req struct {
		Operation string `json:"operation"`
		Objects   []struct {
			Oid  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).
		Decode(&req); err != nil ||
		req.Operation != "upload" {
		hfError(w, http.StatusBadRequest, "", "only LFS uploads are supported")
		return
	}

	if len(req.Objects) > hfLFSBatchMaxObjects {
		hfError(
			w,
			http.StatusRequestEntityTooLarge,
			"",
			"a batch holds at most "+strconv.Itoa(hfLFSBatchMaxObjects)+" objects",
		)

		return
	}

	user := auth.FromContext(r.Context()).Name()

	objects := make([]map[string]any, 0, len(req.Objects))
	for _, o := range req.Objects {
		obj := map[string]any{
			"oid":  o.Oid,
			"size": o.Size,
		}
		if !h.hf.StagedLFS(r.Context(), target, o.Oid) {
			p := m.Prefix + url.PathEscape(target) + "/lfs/" + url.PathEscape(o.Oid)
			obj["actions"] = map[string]any{
				"upload": map[string]string{
					"href": origin(r) + p + "?" + h.tokens.SignPath(p, user, hfLFSUploadTTL),
				},
			}
		}

		objects = append(objects, obj)
	}

	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	w.WriteHeader(http.StatusOK)
	//nolint:errchkjson // client gone
	_ = json.NewEncoder(w).
		Encode(map[string]any{
			"transfer": "basic",
			"objects":  objects,
		})
}

// Write permission was checked when the batch call issued the link.
func (h *Handler) hfLFSUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	user, err := h.tokens.VerifyPath(r.URL.EscapedPath(), q.Get("user"), q.Get("exp"), q.Get("kid"), q.Get("sig"))
	if err != nil {
		hfError(w, http.StatusForbidden, "", "invalid or expired upload link")
		return
	}

	repoKey, oid := r.PathValue("repo"), r.PathValue("oid")
	if err := h.hf.StageLFS(r.Context(), repoKey, oid, io.LimitReader(r.Body, 64<<30), user); h.hfFail(w, r, err) {
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (h *Handler) hfCommit(
	w http.ResponseWriter,
	r *http.Request,
	m Mount,
	rp repository.Repository,
	kind hf.Kind,
	id, rev string,
) {
	target, ok := h.hfUploadTarget(w, r, rp, string(kind)+"/"+id)
	if !ok {
		return
	}

	release := reserveBuffer(w, r, hfMaxCommit, 2)
	if release == nil {
		return
	}
	defer release()

	body, err := io.ReadAll(io.LimitReader(r.Body, hfMaxCommit+1))
	if err != nil || len(body) > hfMaxCommit {
		hfError(w, http.StatusBadRequest, "", "commit unreadable or too large")
		return
	}

	ops, err := hf.ParseCommit(body)
	if h.hfFail(w, r, err) {
		return
	}

	c, err := h.hf.CreateCommit(r.Context(), target, kind, id, rev, ops, auth.FromContext(r.Context()).Name())
	if h.hfFail(w, r, err) {
		return
	}

	commitURL := m.base(r) + "/" + rp.Name + "/"
	if kind == "datasets" {
		commitURL += "datasets/"
	}

	commitURL += id + "/commit/" + c.Oid
	writeJSON(w, http.StatusOK, map[string]string{
		"commitUrl": commitURL,
		"commitOid": c.Oid,
	})
}
