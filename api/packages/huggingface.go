package packages

import (
	"crypto/sha1" //nolint:gosec // folder ids are 40-hex like git's; not a security use
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
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
	hfDefaultRevision    = "main"
)

func (h *Handler) hfAPI(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	repoKey, rest, _ := strings.Cut(rest, "/")

	rp, err := h.repos.GetRepository(r.Context(), repoKey)
	if err != nil || rp.Format != hf.Format {
		hfError(w, http.StatusNotFound, "RepoNotFound", "repository not found")
		return
	}

	_, escapedRest, _ := strings.Cut(strings.TrimPrefix(r.URL.EscapedPath(), m.Prefix), "/")

	parts, ok := hfSegments(escapedRest)
	if !ok {
		hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
		return
	}

	switch {
	case rest == "api/validate-yaml" && r.Method == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{
			"errors":   []string{},
			"warnings": []string{},
		})
	case rest == "api/repos/create" && r.Method == http.MethodPost:
		h.hfCreateRepo(w, r, m, rp)
	case rest == "api/whoami-v2" && r.Method == http.MethodGet:
		h.hfWhoami(w, r)
	case rest == "api/repos/delete" || rest == "api/repos/move":
		hfNotSupported(w, "deleting and moving repositories")
	case len(parts) == 2 && parts[0] == "api" && (parts[1] == "models" || parts[1] == "datasets"):
		hfNotSupported(w, "listing repositories")
	case len(parts) >= 3 && parts[0] == "api":
		kind, ok := hf.ParseKind(parts[1])

		id, op, arg, found := hfSplit(
			parts[2:],
			"revision",
			"tree",
			"preupload",
			"commit",
			"xet-read-token",
			"xet-write-token",
			"refs",
			"commits",
			"paths-info",
			"branch",
			"tag",
		)
		if bare := strings.Join(parts[2:], "/"); !found && len(parts) <= 4 && hf.ValidID(bare) {
			id, op, arg, found = bare, "revision", []string{
				hfDefaultRevision,
			}, true
		}

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
		if !found || len(arg) == 0 || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
			return
		}

		h.hfResolve(w, r, rp, kind, id, arg[0], strings.Join(arg[1:], "/"))
	}
}

// hfSegments splits an escaped path into its unescaped segments, so a
// revision or path huggingface_hub sends with an escaped "/" stays one segment.
func hfSegments(escaped string) ([]string, bool) {
	segments := strings.Split(escaped, "/")
	for i, segment := range segments {
		unescaped, err := url.PathUnescape(segment)
		if err != nil {
			return nil, false
		}

		segments[i] = unescaped
	}

	return segments, true
}

func hfSplit(p []string, ops ...string) (id, op string, rest []string, ok bool) {
	for n := 1; n <= 2 && n < len(p); n++ {
		for _, o := range ops {
			if p[n] == o {
				id = strings.Join(p[:n], "/")
				if !hf.ValidID(id) {
					return "", "", nil, false
				}

				return id, o, p[n+1:], true
			}
		}
	}

	return "", "", nil, false
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
	id, op string,
	arg []string,
) {
	switch {
	case (op == "revision" || op == "tree" && len(arg) > 0) && r.Method == http.MethodGet:
		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			string(kind) + "/" + id,
			auth.ActionRead,
		}) {
			return
		}

		if op == "revision" {
			h.hfRevision(w, r, rp, kind, id, strings.Join(arg, "/"), nil)
			return
		}

		recursive, _ := strconv.ParseBool(r.URL.Query().Get("recursive"))
		h.hfRevision(w, r, rp, kind, id, arg[0], &hfTreeQuery{
			dir:       strings.Join(arg[1:], "/"),
			recursive: recursive,
		})
	case (op == "refs" && len(arg) == 0 || op == "commits") && r.Method == http.MethodGet ||
		op == "paths-info" && r.Method == http.MethodPost:
		if !h.RequireRepo(w, r, RepoCheck{
			rp.Name,
			string(kind) + "/" + id,
			auth.ActionRead,
		}) {
			return
		}

		rev := strings.Join(arg, "/")
		if rev == "" {
			rev = hfDefaultRevision
		}

		switch op {
		case "refs":
			h.hfRefs(w, r, rp, kind, id)
		case "commits":
			h.hfCommits(w, r, rp, kind, id, rev)
		default:
			h.hfPathsInfo(w, r, rp, kind, id, rev)
		}
	case op == "branch" || op == "tag":
		hfNotSupported(w, "creating and deleting branches and tags")
	case op == "preupload" && r.Method == http.MethodPost:
		h.hfPreupload(w, r, rp)
	case op == "commit" && r.Method == http.MethodPost:
		h.hfCommit(w, r, m, rp, kind, id, strings.Join(arg, "/"))
	case (op == "xet-read-token" || op == "xet-write-token") && r.Method == http.MethodGet:
		rev := strings.Join(arg, "/")
		if rev == "" {
			rev = hfDefaultRevision
		}

		h.hfXetTokenIssue(w, r, m, rp, kind, id, op, rev)
	default:
		hfError(w, http.StatusNotFound, "EntryNotFound", "not found")
	}
}

// hfTreeQuery is the folder a tree request lists, and whether below it.
type hfTreeQuery struct {
	dir       string
	recursive bool
}

// hfTreeEntries is the listing of q.dir from every entry of a commit's
// tree, with the folders the files are in as directory entries; found is
// false when q.dir is not in the commit.
func hfTreeEntries(all []map[string]any, q hfTreeQuery) (entries []map[string]any, found bool) {
	prefix := ""
	if q.dir != "" {
		prefix = q.dir + "/"
	}

	listed := map[string]bool{}
	entries = []map[string]any{}
	found = q.dir == ""

	listDirectory := func(p string) {
		if listed[p] {
			return
		}

		listed[p] = true
		entries = append(entries, map[string]any{
			"type": "directory",
			"path": p,
			"oid":  hfTreeOid(p),
			"size": 0,
		})
	}

	for _, e := range all {
		p, _ := e["path"].(string)

		rel, ok := strings.CutPrefix(p, prefix)
		if p == q.dir {
			found = found || e["type"] == "directory"
			continue
		}

		if !ok {
			continue
		}

		found = true

		segments := strings.Split(rel, "/")
		if !q.recursive && len(segments) > 1 {
			listDirectory(prefix + segments[0])
			continue
		}

		for i := 1; q.recursive && i < len(segments); i++ {
			listDirectory(prefix + strings.Join(segments[:i], "/"))
		}

		if e["type"] == "directory" {
			if listed[p] {
				continue
			}

			listed[p] = true
		}

		entries = append(entries, e)
	}

	return entries, found
}

func hfTreeOid(p string) string {
	sum := sha1.Sum([]byte(p)) //nolint:gosec // a folder's id, not a security use

	return hex.EncodeToString(sum[:])
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
	tree *hfTreeQuery,
) {
	info, all, err := h.hfRevisionFiles(r, rp, kind, id, rev, tree != nil)
	if h.hfFail(w, r, err) {
		return
	}

	if tree == nil {
		writeJSON(w, http.StatusOK, info)
		return
	}

	entries, found := hfTreeEntries(all, *tree)
	if !found {
		h.hfFail(w, r, hf.ErrEntryNotFound)
		return
	}

	writeJSON(w, http.StatusOK, entries)
}

func (h *Handler) hfRevisionFiles(
	r *http.Request,
	rp repository.Repository,
	kind hf.Kind,
	id, rev string,
	withFiles bool,
) (map[string]any, []map[string]any, error) {
	lastErr := error(hf.ErrRepoNotFound)

	for _, m := range h.hfMembers(r, rp) {
		var all []map[string]any

		if m.Class != "remote" {
			c, err := h.hf.Revision(r.Context(), m.Name, kind, id, rev)
			if err != nil {
				lastErr = err
				continue
			}

			files, err := h.hf.Files(r.Context(), m.Name, c)
			if err != nil {
				return nil, nil, err
			}

			for _, f := range files {
				all = append(
					all,
					map[string]any{
						"type": "file",
						"path": f.Path,
						"size": f.Item.Size,
						"oid":  f.Item.Checksums.SHA1,
					},
				)
			}

			return hf.Info(c, files), all, nil
		}

		info, err := h.hf.RemoteInfo(r.Context(), m, kind, id, rev)
		if err != nil {
			lastErr = err
			continue
		}

		if !withFiles {
			return info, nil, nil
		}

		sha, _ := info["sha"].(string)

		upstream, err := h.hf.RemoteTree(r.Context(), m, kind, id, sha)
		if err != nil {
			lastErr = err
			continue
		}

		for _, e := range upstream {
			if fields, ok := e.(map[string]any); ok {
				all = append(all, fields)
			}
		}

		return info, all, nil
	}

	return nil, nil, lastErr
}

func hfNotSupported(w http.ResponseWriter, what string) {
	hfError(w, http.StatusNotImplemented, "NotSupported", "Relic does not support "+what)
}

func (h *Handler) hfWhoami(w http.ResponseWriter, r *http.Request) {
	p := auth.FromContext(r.Context())
	if p.Anonymous() {
		hfError(w, http.StatusUnauthorized, "", "Invalid user token.")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"type":     "user",
		"id":       p.Username,
		"name":     p.Username,
		"fullname": p.Username,
		"isPro":    false,
		"orgs":     []any{},
		"auth": map[string]any{
			"type": "access_token",
			"accessToken": map[string]any{
				"displayName": "relic",
				"role":        "write",
			},
		},
	})
}

func (h *Handler) hfRefs(w http.ResponseWriter, r *http.Request, rp repository.Repository, kind hf.Kind, id string) {
	lastErr := error(hf.ErrRepoNotFound)

	for _, m := range h.hfMembers(r, rp) {
		if m.Class == "remote" {
			doc, err := h.hf.RemoteAPI(r.Context(), m, kind, id, "refs")
			if err != nil {
				lastErr = err
				continue
			}

			writeJSON(w, http.StatusOK, doc)

			return
		}

		refs, err := h.hf.Refs(r.Context(), m.Name, kind, id)
		if err != nil {
			lastErr = err
			continue
		}

		branches := make([]map[string]string, 0, len(refs))
		for _, name := range slices.Sorted(maps.Keys(refs)) {
			branches = append(branches, map[string]string{
				"name":         name,
				"ref":          "refs/heads/" + name,
				"targetCommit": refs[name].Oid,
			})
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"branches":     branches,
			"tags":         []any{},
			"converts":     []any{},
			"pullRequests": []any{},
		})

		return
	}

	h.hfFail(w, r, lastErr)
}

func (h *Handler) hfCommits(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	kind hf.Kind,
	id, rev string,
) {
	lastErr := error(hf.ErrRepoNotFound)

	for _, m := range h.hfMembers(r, rp) {
		if m.Class == "remote" {
			doc, err := h.hf.RemoteAPI(r.Context(), m, kind, id, "commits/"+rev)
			if err != nil {
				lastErr = err
				continue
			}

			writeJSON(w, http.StatusOK, doc)

			return
		}

		history, err := h.hf.History(r.Context(), m.Name, kind, id, rev)
		if err != nil {
			lastErr = err
			continue
		}

		commits := make([]map[string]any, 0, len(history))
		for _, c := range history {
			commits = append(commits, map[string]any{
				"id":      c.Oid,
				"authors": []any{},
				"date":    c.Time.Format("2006-01-02T15:04:05.000Z"),
				"title":   "",
				"message": "",
			})
		}

		writeJSON(w, http.StatusOK, commits)

		return
	}

	h.hfFail(w, r, lastErr)
}

const (
	hfMaxPathsInfoBody = 1 << 20
	hfMaxRequestBody   = 16 << 20
)

func (h *Handler) hfPathsInfo(
	w http.ResponseWriter,
	r *http.Request,
	rp repository.Repository,
	kind hf.Kind,
	id, rev string,
) {
	r.Body = http.MaxBytesReader(w, r.Body, hfMaxPathsInfoBody)
	if err := r.ParseForm(); err != nil {
		hfError(w, http.StatusBadRequest, "", "invalid paths-info body")
		return
	}

	_, all, err := h.hfRevisionFiles(r, rp, kind, id, rev, true)
	if h.hfFail(w, r, err) {
		return
	}

	entries, _ := hfTreeEntries(all, hfTreeQuery{
		recursive: true,
	})

	wanted := map[string]bool{}
	for _, p := range r.PostForm["paths"] {
		wanted[strings.Trim(p, "/")] = true
	}

	found := []map[string]any{}

	for _, e := range entries {
		if p, _ := e["path"].(string); wanted[p] {
			found = append(found, e)
		}
	}

	writeJSON(w, http.StatusOK, found)
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

	target, ok := h.hfUploadTarget(w, r, rp, string(kind)+"/"+id)
	if !ok {
		return
	}

	path := id
	if kind == "datasets" {
		path = "datasets/" + id
	}

	created := map[string]any{
		"url":  m.base(r) + "/" + rp.Name + "/" + path,
		"name": id,
	}

	_, err := h.hf.CreateRepository(r.Context(), target, kind, id, auth.FromContext(r.Context()).Name())
	if errors.Is(err, hf.ErrRepoExists) {
		message := "You already created this " + strings.TrimSuffix(string(kind), "s") + " repo"
		created["error"] = message
		w.Header().Set("X-Error-Message", message)
		writeJSON(w, http.StatusConflict, created)

		return
	}

	if h.hfFail(w, r, err) {
		return
	}

	writeJSON(w, http.StatusOK, created)
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

	release := h.buffers.reserve(w, r, hfMaxRequestBody, 2)
	if release == nil {
		return
	}
	defer release()

	var req struct {
		Files []struct {
			Path string `json:"path"`
			Size int64  `json:"size"`
		} `json:"files"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, hfMaxRequestBody)).Decode(&req); err != nil {
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

	release := h.buffers.reserve(w, r, hfMaxRequestBody, 2)
	if release == nil {
		return
	}
	defer release()

	var req struct {
		Operation string `json:"operation"`
		Objects   []struct {
			Oid  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"objects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, hfMaxRequestBody)).
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

	release := h.buffers.reserve(w, r, hfMaxCommit, 2)
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
