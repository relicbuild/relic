package packages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"uuid"

	"relic/internal/access/auth"
	"relic/internal/artifacts/formats/docker"
	"relic/internal/artifacts/repository"
)

type dockerReq struct {
	mount      Mount
	base       string
	repo       string
	repoInBase bool
	image      string
	op         string
	arg        string
}

func (h *Handler) dockerV2(w http.ResponseWriter, r *http.Request, m Mount) {
	rest := strings.TrimPrefix(r.URL.Path, m.Prefix)
	h.ServeRegistry(w, r, m, m.Prefix, "", rest)
}

// ServeRegistry serves the registry API path rest (below "v2/") at base,
// the URL prefix up to and including "v2/".
func (h *Handler) ServeRegistry(w http.ResponseWriter, r *http.Request, m Mount, base, repoKey, rest string) {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")

	switch rest {
	case "":
		if auth.FromContext(r.Context()).Anonymous() {
			dockerChallenge(w)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{})

		return
	case "_catalog":
		h.dockerCatalog(w, r, repoKey)
		return
	}

	req, ok := parseDockerPath(rest)
	if !ok {
		RegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	req.mount = m
	if repoKey != "" {
		req.image = strings.TrimSuffix(req.repo+"/"+req.image, "/")
		req.repo, req.base, req.repoInBase = repoKey, base, true
	} else {
		req.base = base + req.repo + "/"
	}

	if !docker.ValidImage(req.image) {
		RegistryError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}

	rp, err := h.repos.GetRepository(r.Context(), req.repo)
	if err != nil || !docker.Serves(rp.Format) {
		RegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	if m.RegistryHeaders != nil {
		m.RegistryHeaders(w.Header(), req.repo)
	}

	switch req.op {
	case "manifests":
		h.dockerManifest(w, r, req, rp)
	case "blobs":
		h.dockerBlob(w, r, req, rp)
	case "uploads":
		h.dockerUpload(w, r, req)
	case "tags":
		h.dockerTags(w, r, req, rp)
	case "referrers":
		h.dockerReferrers(w, r, req, rp)
	}
}

func (h *Handler) dockerReferrers(w http.ResponseWriter, r *http.Request, req dockerReq, rp repository.Repository) {
	if r.Method != http.MethodGet {
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
		return
	}

	if _, ok := docker.ParseDigest(req.arg); !ok {
		RegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}

	if !h.dockerAllowed(w, r, req.repo, req.image, auth.ActionRead) {
		return
	}

	artifactType := r.URL.Query().Get("artifactType")

	var manifests []docker.Referrer

	for _, member := range h.ReadRepos(r.Context(), rp) {
		found, err := h.docker.Referrers(r.Context(), member.Name, req.image, req.arg, artifactType)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		manifests = append(manifests, found...)
	}

	if manifests == nil {
		manifests = []docker.Referrer{}
	}

	if artifactType != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}

	body, err := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.index.v1+json",
		"manifests":     manifests,
	})
	if err != nil {
		h.dockerFail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	_, _ = w.Write(body)
}

func parseDockerPath(rest string) (dockerReq, bool) {
	for _, op := range []struct {
		sep, name string
	}{
		{
			"/blobs/uploads/",
			"uploads",
		},
		{
			"/referrers/",
			"referrers",
		},
		{
			"/manifests/",
			"manifests",
		},
		{
			"/blobs/",
			"blobs",
		},
	} {
		if i := strings.LastIndex(rest, op.sep); i > 0 {
			name, arg := rest[:i], rest[i+len(op.sep):]
			if op.name != "uploads" && (arg == "" || strings.Contains(arg, "/")) {
				continue
			}

			repoKey, image, _ := strings.Cut(name, "/")

			return dockerReq{
				repo:  repoKey,
				image: image,
				op:    op.name,
				arg:   arg,
			}, true
		}
	}

	if name, ok := strings.CutSuffix(rest, "/tags/list"); ok {
		repoKey, image, _ := strings.Cut(name, "/")

		return dockerReq{
			repo:  repoKey,
			image: image,
			op:    "tags",
		}, true
	}

	return dockerReq{}, false
}

func (h *Handler) dockerAllowed(w http.ResponseWriter, r *http.Request, repoKey, path string, a auth.Action) bool {
	ok, err := h.CanRepo(r, repoKey, path, a)
	if err != nil {
		h.writeError(w, r, err)
		return false
	}

	if ok {
		return true
	}

	if auth.FromContext(r.Context()).Anonymous() {
		dockerChallenge(w)
		return false
	}

	RegistryError(w, http.StatusForbidden, "DENIED", "requested access to the resource is denied")

	return false
}

// ReadRepos lists the repositories a pull from rp searches, in order.
func (h *Handler) ReadRepos(ctx context.Context, rp repository.Repository) []repository.Repository {
	if rp.Class != "virtual" {
		return []repository.Repository{
			rp,
		}
	}

	var out []repository.Repository

	seen := map[string]bool{
		rp.Name: true,
	}

	var walk func(repository.Repository, int)

	walk = func(v repository.Repository, depth int) {
		if depth >= repository.MaxVirtualDepth {
			h.log.WarnContext(
				ctx,
				"virtual repository nesting is too deep to resolve",
				"repository",
				v.Name,
				"limit",
				repository.MaxVirtualDepth,
			)

			return
		}

		for _, m := range h.repos.Members(v) {
			if seen[m] {
				continue
			}

			seen[m] = true

			mr, err := h.repos.GetRepository(ctx, m)
			if err != nil {
				continue
			}

			if mr.Class == "virtual" {
				walk(mr, depth+1)
			} else {
				out = append(out, mr)
			}
		}
	}
	walk(rp, 0)

	return out
}

func firstFound[T any](
	ctx context.Context,
	members []repository.Repository,
	unknown error,
	find func(context.Context, repository.Repository) (T, error),
) (T, error) {
	var (
		zero        T
		upstreamErr error
	)

	for _, m := range members {
		v, err := find(ctx, m)
		switch {
		case err == nil:
			return v, nil
		case errors.Is(err, repository.ErrUpstream):
			upstreamErr = err
		case !errors.Is(err, unknown):
			return zero, err
		}
	}

	if upstreamErr != nil {
		return zero, upstreamErr
	}

	return zero, unknown
}

func (h *Handler) dockerManifest(w http.ResponseWriter, r *http.Request, req dockerReq, rp repository.Repository) {
	ref := req.arg
	if _, isDigest := docker.ParseDigest(ref); !isDigest && !docker.ValidTag(ref) {
		// A reference that cannot name a manifest names none that exists;
		// only a push of one is invalid.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			RegistryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
			return
		}

		RegistryError(w, http.StatusBadRequest, "TAG_INVALID", "manifest tag did not match URI")

		return
	}

	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !h.dockerAllowed(w, r, req.repo, req.image+"/"+ref, auth.ActionRead) {
			return
		}

		m, err := firstFound(
			r.Context(),
			h.ReadRepos(r.Context(), rp),
			docker.ErrManifestUnknown,
			func(ctx context.Context, member repository.Repository) (docker.Manifest, error) {
				return h.docker.Manifest(ctx, member, req.image, ref)
			},
		)
		if err == nil {
			var servable bool
			if servable, err = h.CanServe(r, m.Item); err == nil && !servable {
				err = docker.ErrManifestUnknown
			}
		}

		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		if !h.allowImage(w, r, m.Item) {
			return
		}

		w.Header().Set("Content-Type", m.MediaType)
		w.Header().Set("Docker-Content-Digest", m.Digest)
		w.Header().Set("Content-Length", strconv.FormatInt(m.Item.Size, 10))
		w.Header().Set("Etag", `"`+m.Digest+`"`)

		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}

		h.dockerStream(w, r, m.Item)
	case http.MethodPut:
		target, err := h.repos.DeployTarget(r.Context(), req.repo)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		if !h.dockerAllowed(w, r, target, req.image+"/"+ref, auth.ActionWrite) {
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, docker.MaxManifestBytes+1))
		if err != nil {
			RegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", "manifest invalid")
			return
		}

		if len(body) > docker.MaxManifestBytes {
			RegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", "manifest too large")
			return
		}

		var from []repository.Repository

		if target != req.repo {
			if ok, err := h.CanRepo(r, req.repo, req.image, auth.ActionRead); err == nil && ok {
				from = h.ReadRepos(r.Context(), rp)
			}
		}

		digest, err := h.docker.PutManifest(
			r.Context(),
			target,
			req.image,
			ref,
			r.Header.Get("Content-Type"),
			body,
			auth.FromContext(r.Context()).Name(),
			func(it repository.Item) bool {
				servable, err := h.CanServe(r, it)
				return err == nil && servable
			},
			from...)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		var subject struct {
			Subject *struct {
				Digest string `json:"digest"`
			} `json:"subject"`
		}
		if json.Unmarshal(body, &subject) == nil && subject.Subject != nil {
			w.Header().Set("OCI-Subject", subject.Subject.Digest)
		}

		w.Header().Set("Location", req.base+req.image+"/manifests/"+digest)
		w.Header().Set("Docker-Content-Digest", digest)
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		target, err := h.repos.DeployTarget(r.Context(), req.repo)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		if !h.dockerAllowed(w, r, target, req.image+"/"+ref, auth.ActionDelete) {
			return
		}

		if err := h.docker.DeleteManifest(r.Context(), target, req.image, ref); err != nil {
			h.dockerFail(w, r, err)
			return
		}

		w.WriteHeader(http.StatusAccepted)
	default:
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
	}
}

func (h *Handler) dockerBlob(w http.ResponseWriter, r *http.Request, req dockerReq, rp repository.Repository) {
	hexDigest, ok := docker.ParseDigest(req.arg)
	if !ok {
		RegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "provided digest did not match uploaded content")
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
		return
	}

	if !h.dockerAllowed(w, r, req.repo, req.image, auth.ActionRead) {
		return
	}

	item, err := firstFound(
		r.Context(),
		h.ReadRepos(r.Context(), rp),
		docker.ErrBlobUnknown,
		func(ctx context.Context, member repository.Repository) (repository.Item, error) {
			return h.docker.BlobIn(ctx, member, req.image, hexDigest)
		},
	)
	if err == nil {
		var servable bool
		if servable, err = h.CanServe(r, item); err == nil && !servable {
			err = docker.ErrBlobUnknown
		}
	}

	if err != nil {
		h.dockerFail(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Docker-Content-Digest", req.arg)
	w.Header().Set("Content-Length", strconv.FormatInt(item.Size, 10))
	// Clients read these on HEAD for remote-cache markers.
	w.Header().Set("X-Checksum-Sha1", item.Checksums.SHA1)
	w.Header().Set("X-Checksum-Md5", item.Checksums.MD5)
	w.Header().Set("X-Checksum-Sha256", item.Checksums.SHA256)
	req.mount.fileHeaders(w.Header(), item)

	if !h.allowDownload(w, r, item) {
		return
	}

	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}

	h.dockerStream(w, r, item)
}

func (h *Handler) dockerStream(w http.ResponseWriter, r *http.Request, item repository.Item) {
	rc, err := h.repos.Open(r.Context(), item, nil)
	if err != nil {
		w.Header().Del("Content-Length")
		h.dockerFail(w, r, err)

		return
	}
	defer func() {
		_ = rc.Close()
	}()

	w.WriteHeader(http.StatusOK)

	if _, err := copyBody(w, rc); err != nil {
		panic(http.ErrAbortHandler)
	}
}

const maxUploadChunks = docker.MaxUploadChunks

func (h *Handler) dockerUpload(w http.ResponseWriter, r *http.Request, req dockerReq) {
	target, err := h.repos.DeployTarget(r.Context(), req.repo)
	if err != nil {
		h.dockerFail(w, r, err)
		return
	}

	if !h.dockerAllowed(w, r, target, req.image, auth.ActionWrite) {
		return
	}

	user := auth.FromContext(r.Context()).Name()
	if req.arg == "" {
		if r.Method != http.MethodPost {
			RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
			return
		}

		h.dockerStartUpload(w, r, req, target, user)

		return
	}

	id, err := uuid.Parse(req.arg)
	if err != nil {
		RegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown to registry")
		return
	}

	chunks, offset, ok := parseUploadState(r.URL.Query().Get("_state"))
	if !ok {
		RegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", "blob upload unknown to registry")
		return
	}

	if err := h.docker.FindUpload(r.Context(), id.String()); err != nil {
		h.dockerFail(w, r, err)
		return
	}

	if r.Method != http.MethodPut {
		if chunks, offset, err = h.docker.UploadState(r.Context(), id.String(), chunks, offset); err != nil {
			h.dockerFail(w, r, err)
			return
		}
	}

	switch r.Method {
	case http.MethodGet:
		writeUploadStatus(w, req, id.String(), chunks, offset, http.StatusNoContent)
	case http.MethodPatch:
		// A chunk must start where the upload stands.
		if cr := r.Header.Get("Content-Range"); cr != "" {
			start, _, _ := strings.Cut(strings.TrimPrefix(cr, "bytes="), "-")
			if n, err := strconv.ParseInt(start, 10, 64); err != nil || n != offset {
				w.Header().
					Set("Location", fmt.Sprintf("%s%s/blobs/uploads/%s?_state=%d.%d", req.base, req.image, id, chunks, offset))
				w.Header().Set("Range", fmt.Sprintf("0-%d", max(offset-1, 0)))
				RegistryError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "chunk out of order")

				return
			}
		}

		if chunks >= maxUploadChunks {
			RegistryError(w, http.StatusRequestedRangeNotSatisfiable, "BLOB_UPLOAD_INVALID", "too many chunks")
			return
		}

		limit, err := h.repos.UploadLimit(r.Context(), target)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		body := io.Reader(r.Body)

		var limited *repository.LimitedReader

		if limit > 0 {
			if offset+r.ContentLength > limit {
				h.dockerFail(w, r, repository.ErrTooLarge)
				return
			}

			limited = repository.LimitedBody(r.Body, limit-offset)
			body = limited
		}

		n, err := h.docker.AppendChunk(r.Context(), id.String(), chunks, body, r.ContentLength)
		if limited != nil && limited.Exceeded() {
			err = repository.ErrTooLarge
		}

		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		writeUploadStatus(w, req, id.String(), chunks+1, offset+n, http.StatusAccepted)
	case http.MethodPut:
		digest := r.URL.Query().Get("digest")

		hexDigest, ok := docker.ParseDigest(digest)
		if !ok {
			RegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "provided digest did not match uploaded content")
			return
		}

		size := int64(-1)
		if r.ContentLength >= 0 {
			size = offset + r.ContentLength
		}

		if _, err := h.docker.FinishUpload(
			r.Context(),
			target,
			req.image,
			id.String(),
			chunks,
			r.Body,
			size,
			hexDigest,
			user,
		); err != nil {
			h.dockerFail(w, r, err)
			return
		}

		writeBlobCreated(w, req, digest)
	case http.MethodDelete:
		h.docker.CancelUpload(r.Context(), id.String(), chunks)
		w.WriteHeader(http.StatusNoContent)
	default:
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
	}
}

func (h *Handler) dockerStartUpload(w http.ResponseWriter, r *http.Request, req dockerReq, target, user string) {
	q := r.URL.Query()
	if mount, ok := docker.ParseDigest(q.Get("mount")); ok {
		if it, err := h.docker.Blob(r.Context(), target, req.image, mount); err == nil {
			servable, err := h.CanServe(r, it)
			if err != nil {
				h.dockerFail(w, r, err)
				return
			}

			if servable {
				writeBlobCreated(w, req, q.Get("mount"))
				return
			}
		}
	}

	id := uuid.NewV4().String()

	if digest := q.Get("digest"); digest != "" {
		hexDigest, ok := docker.ParseDigest(digest)
		if !ok {
			RegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", "provided digest did not match uploaded content")
			return
		}

		if _, err := h.docker.FinishUpload(
			r.Context(),
			target,
			req.image,
			id,
			0,
			r.Body,
			r.ContentLength,
			hexDigest,
			user,
		); err != nil {
			h.dockerFail(w, r, err)
			return
		}

		writeBlobCreated(w, req, digest)

		return
	}

	if err := h.docker.StartUpload(r.Context(), id); err != nil {
		h.dockerFail(w, r, err)
		return
	}

	writeUploadStatus(w, req, id, 0, 0, http.StatusAccepted)
}

func parseUploadState(s string) (chunks int, offset int64, ok bool) {
	if s == "" {
		return 0, 0, true
	}

	c, o, found := strings.Cut(s, ".")
	chunks, err1 := strconv.Atoi(c)

	offset, err2 := strconv.ParseInt(o, 10, 64)
	if !found || err1 != nil || err2 != nil || chunks < 0 || chunks > maxUploadChunks || offset < 0 {
		return 0, 0, false
	}

	return chunks, offset, true
}

func writeUploadStatus(w http.ResponseWriter, req dockerReq, id string, chunks int, offset int64, status int) {
	w.Header().
		Set("Location", fmt.Sprintf("%s%s/blobs/uploads/%s?_state=%d.%d", req.base, req.image, id, chunks, offset))
	w.Header().Set("Docker-Upload-UUID", id)
	w.Header().Set("Range", fmt.Sprintf("0-%d", max(offset-1, 0)))
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

func writeBlobCreated(w http.ResponseWriter, req dockerReq, digest string) {
	w.Header().Set("Location", req.base+req.image+"/blobs/"+digest)
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(http.StatusCreated)
}

func (h *Handler) dockerTags(w http.ResponseWriter, r *http.Request, req dockerReq, rp repository.Repository) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "the operation is unsupported")
		return
	}

	if !h.dockerAllowed(w, r, req.repo, req.image, auth.ActionRead) {
		return
	}

	var tags []string

	known := false

	for _, member := range h.ReadRepos(r.Context(), rp) {
		image := req.image
		if member.Class == "remote" {
			image = docker.UpstreamImage(h.repos.UpstreamOf(member).URL, image)
		}

		_, err := h.repos.Stat(r.Context(), member.Name, image)
		switch {
		case err == nil:
			known = true
		case errors.Is(err, repository.ErrNotFound):
			continue
		default:
			h.dockerFail(w, r, err)
			return
		}

		t, err := h.docker.Tags(r.Context(), member.Name, image)
		if err != nil {
			h.dockerFail(w, r, err)
			return
		}

		tags = append(tags, t...)
	}

	if !known {
		RegistryError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}

	slices.Sort(tags)

	tags = paginate(slices.Compact(tags), r)
	if tags == nil {
		tags = []string{}
	}

	name := req.image
	if !req.repoInBase {
		name = req.repo + "/" + req.image
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"name": name,
		"tags": tags,
	})
}

func (h *Handler) dockerCatalog(w http.ResponseWriter, r *http.Request, only string) {
	all, err := h.repos.ListRepositories(r.Context(), "", "")
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	repos := slices.DeleteFunc(all, func(rp repository.Repository) bool {
		return !docker.Serves(rp.Format)
	})

	var names []string

	for _, rp := range repos {
		if only != "" && rp.Name != only {
			continue
		}

		for _, member := range h.ReadRepos(r.Context(), rp) {
			images, err := h.docker.Images(r.Context(), member.Name)
			if err != nil {
				h.writeError(w, r, err)
				return
			}

			for _, image := range images {
				if ok, err := h.CanRepo(r, rp.Name, image, auth.ActionRead); err != nil || !ok {
					continue
				}

				if only != "" {
					names = append(names, image)
				} else {
					names = append(names, rp.Name+"/"+image)
				}
			}
		}
	}

	slices.Sort(names)

	names = paginate(slices.Compact(names), r)
	if names == nil {
		names = []string{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"repositories": names,
	})
}

func paginate(list []string, r *http.Request) []string {
	if last := r.URL.Query().Get("last"); last != "" {
		i, _ := slices.BinarySearch(list, last)
		for i < len(list) && list[i] <= last {
			i++
		}

		list = list[i:]
	}

	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n >= 0 && n < len(list) {
		list = list[:n]
	}

	return list
}

func (h *Handler) dockerFail(w http.ResponseWriter, r *http.Request, err error) {
	var unknown docker.BlobUnknownError
	switch {
	case errors.As(err, &unknown):
		RegistryError(w, http.StatusBadRequest, "MANIFEST_BLOB_UNKNOWN", unknown.Error())
	case errors.Is(err, docker.ErrManifestUnknown):
		RegistryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", err.Error())
	case errors.Is(err, docker.ErrBlobUnknown):
		RegistryError(w, http.StatusNotFound, "BLOB_UNKNOWN", err.Error())
	case errors.Is(err, docker.ErrManifestInvalid):
		RegistryError(w, http.StatusBadRequest, "MANIFEST_INVALID", err.Error())
	case errors.Is(err, docker.ErrDigestInvalid):
		RegistryError(w, http.StatusBadRequest, "DIGEST_INVALID", err.Error())
	case errors.Is(err, docker.ErrUploadUnknown):
		RegistryError(w, http.StatusNotFound, "BLOB_UPLOAD_UNKNOWN", err.Error())
	case errors.Is(err, repository.ErrNotLocal):
		RegistryError(w, http.StatusMethodNotAllowed, "UNSUPPORTED", "repository does not accept pushes")
	case errors.Is(err, repository.ErrTooLarge):
		RegistryError(w, http.StatusRequestEntityTooLarge, "SIZE_INVALID", err.Error())
	case errors.Is(err, repository.ErrHeld), errors.Is(err, repository.ErrImmutable):
		RegistryError(w, http.StatusForbidden, "DENIED", err.Error())
	case errors.Is(err, repository.ErrQuotaExceeded):
		RegistryError(w, http.StatusRequestEntityTooLarge, "DENIED", err.Error())
	default:
		h.writeError(w, r, err)
	}
}

func dockerChallenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Relic"`)
	RegistryError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
}

type dockerErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RegistryError answers a container registry error in the registry's
// error format.
func RegistryError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string][]dockerErrorBody{
		"errors": {
			{
				Code:    code,
				Message: msg,
			},
		},
	})
}

var registryErrorCodes = map[int]string{
	http.StatusUnauthorized:          "UNAUTHORIZED",
	http.StatusForbidden:             "DENIED",
	http.StatusMethodNotAllowed:      "UNSUPPORTED",
	http.StatusRequestEntityTooLarge: "SIZE_INVALID",
	http.StatusTooManyRequests:       "TOOMANYREQUESTS",
}

// RegistryStatusError answers an error the HTTP layer raises before the
// registry runs (bad credentials, rate limits) in the registry's error
// format.
func RegistryStatusError(w http.ResponseWriter, status int, message string) {
	code, ok := registryErrorCodes[status]
	if !ok {
		code = "UNKNOWN"
	}

	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	RegistryError(w, status, code, message)
}

func (h *Handler) allowImage(w http.ResponseWriter, r *http.Request, item repository.Item) bool {
	if h.blocker == nil {
		return true
	}

	d, err := h.blocker.Check(r.Context(), item.Repository, item.Path(), item.Checksums.SHA256)
	if err != nil {
		h.writeError(w, r, err)
		return false
	}

	if d.Blocked {
		RegistryError(w, http.StatusForbidden, "DENIED", "image download blocked by Relic Scan policy: "+d.Reason)
		return false
	}

	return true
}
