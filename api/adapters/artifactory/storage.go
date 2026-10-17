package artifactory

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"relic/api/adapters/artifactory/layout"
	"relic/api/adapters/artifactory/wire"
	"relic/api/packages"
	"relic/internal/access/auth"
	"relic/internal/artifacts/repository"
)

func (h *Handler) repoPath(r *http.Request, s string) (string, string, error) {
	key, p, _ := strings.Cut(s, "/")

	remote, ok, err := layout.RemoteOfCache(r.Context(), h.repos, key)
	if err != nil {
		return "", "", err
	}

	if ok {
		key = remote
	}

	return key, p, nil
}

func (h *Handler) storageTarget(r *http.Request) (repository.Item, error) {
	repoKey, p, err := h.repoPath(r, r.PathValue("path"))
	if err != nil {
		return repository.Item{}, err
	}

	return h.repos.Stat(r.Context(), repoKey, p)
}

func (h *Handler) shown(r *http.Request, item repository.Item) repository.Item {
	item.Repository = layout.ShownName(r.Context(), h.repos, item.Repository)
	return item
}

func (h *Handler) storageGet(w http.ResponseWriter, r *http.Request) {
	repoKey, p, err := h.repoPath(r, r.PathValue("path"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.packages.RequireRepo(w, r, packages.RepoCheck{
		Repo:   repoKey,
		Path:   p,
		Action: auth.ActionRead,
	}) {
		return
	}

	item, err := h.storageTarget(r)
	if errors.Is(err, repository.ErrNotFound) {
		if gen, _, ok := h.packages.GeneratedFile(r, repoKey, p); ok {
			wire.WriteJSON(w, http.StatusOK, newFileInfo(r, gen))
			return
		}
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	q := rawQuery(r.URL.RawQuery)
	switch {
	case has(q, "properties"):
		h.getProperties(w, r, item, queryValue(q, "properties"))
	case has(q, "list"):
		h.fileList(w, r, item, q)
	case has(q, "stats") && !item.IsFolder():
		h.itemStats(w, r, item)
	case item.IsFolder():
		children, err := h.repos.Children(r.Context(), item)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		wire.WriteJSON(w, http.StatusOK, newFolderInfo(r, h.shown(r, item), children))
	default:
		wire.WriteJSON(w, http.StatusOK, newFileInfo(r, h.shown(r, item)))
	}
}

type itemStats struct {
	URI                  string `json:"uri"`
	DownloadCount        int64  `json:"downloadCount"`
	LastDownloaded       int64  `json:"lastDownloaded"`
	LastDownloadedBy     string `json:"lastDownloadedBy,omitempty"`
	RemoteDownloadCount  int64  `json:"remoteDownloadCount"`
	RemoteLastDownloaded int64  `json:"remoteLastDownloaded"`
}

func (h *Handler) itemStats(w http.ResponseWriter, r *http.Request, item repository.Item) {
	shown := h.shown(r, item)
	out := itemStats{
		URI: baseURL(r) + "/api/storage/" + escapePath(shown.Repository+storagePath(shown)),
	}

	d, err := h.downloads.Of(r.Context(), item.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if d.Count > 0 {
		out.DownloadCount, out.LastDownloaded, out.LastDownloadedBy = d.Count, d.LastDownloadedAt.UnixMilli(), d.LastDownloadedBy
	}

	wire.WriteJSON(w, http.StatusOK, out)
}

func has(q map[string]string, key string) bool {
	_, ok := q[key]
	return ok
}

type itemProperties struct {
	URI        string              `json:"uri"`
	Properties map[string][]string `json:"properties"`
}

func (h *Handler) getProperties(w http.ResponseWriter, r *http.Request, item repository.Item, only string) {
	props, err := h.repos.Properties(r.Context(), item)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if only != "" {
		wanted := map[string]bool{}
		for k := range strings.SplitSeq(only, ",") {
			wanted[k] = true
		}

		for k := range props {
			if !wanted[k] {
				delete(props, k)
			}
		}
	}

	if len(props) == 0 {
		wire.WriteError(w, http.StatusNotFound, "No properties could be found.")
		return
	}

	shown := h.shown(r, item)
	wire.WriteJSON(w, http.StatusOK, itemProperties{
		URI:        baseURL(r) + "/api/storage/" + escapePath(shown.Repository+storagePath(shown)),
		Properties: props,
	})
}

type fileListResponse struct {
	URI     string         `json:"uri"`
	Created string         `json:"created"`
	Files   []fileListFile `json:"files"`
}

type fileListFile struct {
	URI          string `json:"uri"`
	Size         int64  `json:"size"`
	LastModified string `json:"lastModified"`
	Folder       bool   `json:"folder"`
	SHA1         string `json:"sha1,omitempty"`
	SHA2         string `json:"sha2,omitempty"`
}

func (h *Handler) fileList(w http.ResponseWriter, r *http.Request, item repository.Item, q map[string]string) {
	if !item.IsFolder() {
		wire.WriteError(w, http.StatusBadRequest, "The path is a file, not a folder.")
		return
	}

	deep := queryValue(q, "deep") == "1"
	listFolders := queryValue(q, "listFolders") == "1"

	var (
		items []repository.Item
		err   error
	)
	if deep {
		items, err = h.repos.Subtree(r.Context(), item)
	} else {
		items, err = h.repos.Children(r.Context(), item)
	}

	if err != nil {
		h.writeError(w, r, err)
		return
	}

	prefix := ""
	if item.Name != repository.RootPath {
		prefix = item.Path() + "/"
	}

	shown := h.shown(r, item)

	resp := fileListResponse{
		URI:     baseURL(r) + "/api/storage/" + escapePath(shown.Repository+storagePath(shown)),
		Created: formatTime(time.Now()),
		Files:   []fileListFile{},
	}

	for _, it := range items {
		if it.Path() == item.Path() || (it.IsFolder() && !listFolders) {
			continue
		}

		servable, err := h.packages.CanServe(r, it)
		if err != nil {
			h.writeError(w, r, err)
			return
		}

		if !servable {
			continue
		}

		resp.Files = append(resp.Files, fileListFile{
			URI:          "/" + strings.TrimPrefix(it.Path(), prefix),
			Size:         it.Size,
			LastModified: formatTime(it.Modified),
			Folder:       it.IsFolder(),
			SHA1:         it.Checksums.SHA1,
			SHA2:         it.Checksums.SHA256,
		})
	}

	wire.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) storageSetProperties(w http.ResponseWriter, r *http.Request) {
	repoKey, p, err := h.repoPath(r, r.PathValue("path"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.packages.RequireRepo(w, r, packages.RepoCheck{
		Repo:   repoKey,
		Path:   p,
		Action: auth.ActionAnnotate,
	}) {
		return
	}

	item, err := h.storageTarget(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	q := rawQuery(r.URL.RawQuery)

	props, err := parseProps(propertyPairs(q["properties"]))
	if err != nil || len(props) == 0 {
		wire.WriteError(w, http.StatusBadRequest, "properties parameter is missing or invalid")
		return
	}

	if err := h.repos.SetProperties(r.Context(), item, props, recursive(q)); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) storageDeleteProperties(w http.ResponseWriter, r *http.Request) {
	repoKey, p, err := h.repoPath(r, r.PathValue("path"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	if !h.packages.RequireRepo(w, r, packages.RepoCheck{
		Repo:   repoKey,
		Path:   p,
		Action: auth.ActionAnnotate,
	}) {
		return
	}

	item, err := h.storageTarget(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}

	q := rawQuery(r.URL.RawQuery)

	keys := splitValues(queryValue(q, "properties"))
	if len(keys) == 0 || keys[0] == "" {
		wire.WriteError(w, http.StatusBadRequest, "properties parameter is missing")
		return
	}

	if err := h.repos.DeleteProperties(r.Context(), item, keys, recursive(q)); err != nil {
		h.writeError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func recursive(q map[string]string) bool {
	v, ok := q["recursive"]
	return !ok || v != "0"
}

func propertyPairs(raw string) []string {
	if strings.Contains(raw, "=") {
		return strings.Split(raw, ";")
	}

	decoded, err := url.QueryUnescape(raw)
	if err != nil {
		return strings.Split(raw, ";")
	}

	var pairs []string

	for pair := range strings.SplitSeq(decoded, ";") {
		k, v, _ := strings.Cut(pair, "=")
		pairs = append(pairs, url.QueryEscape(k)+"="+url.QueryEscape(v))
	}

	return pairs
}
